package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go-base/internal/auth"
	"go-base/internal/domain"
	"go-base/internal/feed"
	"go-base/internal/inventory"
	"go-base/internal/manure"
)

var testDatabaseSequence atomic.Uint64

func testDatabase(t *testing.T) (*Database, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = "postgres://farm:farm@127.0.0.1:55432/farm?sslmode=disable"
	}
	adminConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	adminConfig.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	databaseName := fmt.Sprintf("herdcycle_test_%d_%d", time.Now().UnixNano(), testDatabaseSequence.Add(1))
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	appURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	appURL.Path = "/" + databaseName
	dsn := appURL.String()
	db, err := Open(ctx, dsn)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		admin.Close()
	})
	return db, dsn
}

func bootstrapDatabase(t *testing.T) *Database {
	t.Helper()
	db, _ := testDatabase(t)
	if err := db.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return db
}

func TestMigrateCreatesEveryVersionAndCanRunAgain(t *testing.T) {
	db, dsn := testDatabase(t)
	ctx := context.Background()
	rows, err := db.Pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if fmt.Sprint(versions) != "[1 2]" {
		t.Fatalf("migration versions = %v", versions)
	}
	for _, table := range []string{
		"users",
		"sessions",
		"feed_plans",
		"manure_batches",
		"outbox_jobs",
		"feed_lots",
		"feed_reservations",
		"feed_ledger",
	} {
		var exists bool
		if err := db.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	reopened.Close()
}

func TestWithTxRollsBackAllWritesOnError(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	want := errors.New("stop transaction")
	err := db.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO barns(id,tenant_id,name,capacity,status) VALUES('rollback-barn','demo','Rollback Barn',10,'active')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO animal_groups(id,tenant_id,barn_id,name,headcount,status) VALUES('rollback-group','demo','rollback-barn','Rollback Group',5,'active')`); err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("WithTx error = %v", err)
	}
	for _, table := range []string{"barns", "animal_groups"} {
		var count int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE id LIKE 'rollback-%'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rolled back rows", table, count)
		}
	}
}

func TestBootstrapUsersAuthenticateAndPasswordHashesAreSalted(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	repo := AuthRepository{DB: db}
	service := auth.Service{Repo: repo, TTL: time.Hour, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	manager, token, err := service.Login(ctx, "demo", " MANAGER@HERD.LOCAL ", "manager-pass")
	if err != nil {
		t.Fatalf("manager login: %v", err)
	}
	if manager.Role != domain.RoleManager || token == "" {
		t.Fatalf("manager = %+v token empty=%v", manager, token == "")
	}
	operator, err := repo.FindUser(ctx, "demo", "operator@herd.local")
	if err != nil {
		t.Fatal(err)
	}
	if manager.PasswordDigest == operator.PasswordDigest {
		t.Fatal("different seeded passwords unexpectedly share a digest")
	}
	if len(manager.PasswordDigest) < 50 || manager.PasswordDigest[:4] != "$2a$" {
		t.Fatalf("manager password is not a bcrypt digest: %q", manager.PasswordDigest)
	}
	got, session, err := service.Authenticate(ctx, token)
	if err != nil || got.ID != manager.ID || session.UserID != manager.ID {
		t.Fatalf("Authenticate() user=%+v session=%+v err=%v", got, session, err)
	}
}

func TestDisabledUserLosesPreviouslyIssuedSession(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	service := auth.Service{Repo: AuthRepository{DB: db}, TTL: time.Hour, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	_, token, err := service.Login(ctx, "demo", "operator@herd.local", "operator-pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE users SET disabled=true WHERE id='op-1'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Authenticate(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestScheduleRollsBackInventoryWhenPlanInsertFails(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	repo := FeedRepository{DB: db}
	plan := domain.FeedPlan{
		ID:           "plan-rollback",
		TenantID:     "demo",
		GroupID:      "group-a",
		OperatorID:   "op-1",
		Status:       "scheduled",
		FeedKg:       125,
		ScheduledFor: time.Now().Add(time.Hour),
		Version:      1,
	}
	input := feed.ScheduleInput{Plan: plan, FeedCode: "TMR-01", ActorID: "mgr-1", RequestID: "req-1", AuditID: "audit-1", OutboxID: "job-1"}
	if _, err := repo.Schedule(ctx, input); err != nil {
		t.Fatalf("first Schedule: %v", err)
	}
	var availableBefore, reservedBefore float64
	if err := db.Pool.QueryRow(ctx, "SELECT available_kg,reserved_kg FROM feed_inventory WHERE tenant_id='demo' AND feed_code='TMR-01'").Scan(&availableBefore, &reservedBefore); err != nil {
		t.Fatal(err)
	}
	input.AuditID = "audit-2"
	input.OutboxID = "job-2"
	if _, err := repo.Schedule(ctx, input); err == nil {
		t.Fatal("duplicate plan schedule succeeded")
	}
	var availableAfter, reservedAfter float64
	if err := db.Pool.QueryRow(ctx, "SELECT available_kg,reserved_kg FROM feed_inventory WHERE tenant_id='demo' AND feed_code='TMR-01'").Scan(&availableAfter, &reservedAfter); err != nil {
		t.Fatal(err)
	}
	if availableAfter != availableBefore || reservedAfter != reservedBefore {
		t.Fatalf("inventory changed across rollback: before=(%v,%v) after=(%v,%v)", availableBefore, reservedBefore, availableAfter, reservedAfter)
	}
	for _, id := range []string{"audit-2", "job-2"} {
		var count int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE id=$1", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("rolled back side effect %s exists", id)
		}
	}
}

func scheduledPlan(t *testing.T, db *Database, id string, amount float64) {
	t.Helper()
	var role string
	var disabled bool
	if err := db.Pool.QueryRow(context.Background(), "SELECT role,disabled FROM users WHERE tenant_id='demo' AND id='op-1'").Scan(&role, &disabled); err != nil {
		t.Fatalf("load seeded operator before scheduling: %v", err)
	}
	if role != string(domain.RoleOperator) || disabled {
		t.Fatalf("seeded operator role=%q disabled=%v", role, disabled)
	}
	input := feed.ScheduleInput{
		Plan: domain.FeedPlan{
			ID:           id,
			TenantID:     "demo",
			GroupID:      "group-a",
			OperatorID:   "op-1",
			Status:       "scheduled",
			FeedKg:       amount,
			ScheduledFor: time.Now().Add(time.Hour),
			Version:      1,
		},
		FeedCode:  "TMR-01",
		ActorID:   "mgr-1",
		RequestID: "req-" + id,
		AuditID:   "audit-schedule-" + id,
		OutboxID:  "job-schedule-" + id,
	}
	if _, err := (FeedRepository{DB: db}).Schedule(context.Background(), input); err != nil {
		t.Fatalf("schedule %s: %v", id, err)
	}
}

func completionInput(planID, suffix string) feed.CompleteInput {
	return feed.CompleteInput{
		TenantID:        "demo",
		PlanID:          planID,
		ActorID:         "op-1",
		RoundID:         "round-" + suffix,
		BatchID:         "batch-" + suffix,
		IdempotencyKey:  "key-" + suffix,
		RequestID:       "req-complete-" + suffix,
		AuditID:         "audit-complete-" + suffix,
		OutboxID:        "job-complete-" + suffix,
		DeliveredKg:     100,
		ManureKg:        45,
		At:              time.Now().UTC(),
		ExpectedVersion: 1,
	}
}

func TestCompletePersistsConnectedWorkflowAndIdempotentReplay(t *testing.T) {
	db := bootstrapDatabase(t)
	scheduledPlan(t, db, "plan-complete", 100)
	ctx := context.Background()
	repo := FeedRepository{DB: db}
	input := completionInput("plan-complete", "complete")
	round, batch, err := repo.Complete(ctx, input)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if round.PlanID != input.PlanID || batch.SourceRoundID != round.ID || batch.GroupID != "group-a" {
		t.Fatalf("round=%+v batch=%+v", round, batch)
	}
	replayed, replayedBatch, err := repo.Complete(ctx, feed.CompleteInput{
		TenantID:        input.TenantID,
		PlanID:          input.PlanID,
		ActorID:         input.ActorID,
		RoundID:         "unused-round",
		BatchID:         "unused-batch",
		IdempotencyKey:  input.IdempotencyKey,
		RequestID:       "unused-request",
		AuditID:         "unused-audit",
		OutboxID:        "unused-job",
		DeliveredKg:     input.DeliveredKg,
		ManureKg:        input.ManureKg,
		At:              input.At.Add(time.Minute),
		ExpectedVersion: input.ExpectedVersion,
	})
	if err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	if replayed.ID != round.ID || replayedBatch.ID != batch.ID {
		t.Fatalf("replay returned different records: round=%+v batch=%+v", replayed, replayedBatch)
	}
	var planStatus string
	var planVersion int64
	if err := db.Pool.QueryRow(ctx, "SELECT status,version FROM feed_plans WHERE id=$1", input.PlanID).Scan(&planStatus, &planVersion); err != nil {
		t.Fatal(err)
	}
	if planStatus != "completed" || planVersion != 2 {
		t.Fatalf("plan status=%s version=%d", planStatus, planVersion)
	}
	var auditCount, outboxCount int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE object_id=$1 AND action='feed.complete'", input.PlanID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox_jobs WHERE topic='manure.batch.collected' AND payload->>'plan_id'=$1", input.PlanID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || outboxCount != 1 {
		t.Fatalf("audit count=%d outbox count=%d", auditCount, outboxCount)
	}
}

func TestConcurrentCompletionCreatesOnlyOneRound(t *testing.T) {
	db := bootstrapDatabase(t)
	scheduledPlan(t, db, "plan-race", 100)
	repo := FeedRepository{DB: db}
	start := make(chan struct{})
	errorsByAttempt := make(chan error, 2)
	var wait sync.WaitGroup
	for _, suffix := range []string{"race-a", "race-b"} {
		suffix := suffix
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := repo.Complete(context.Background(), completionInput("plan-race", suffix))
			errorsByAttempt <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByAttempt)
	var succeeded, failed int
	for err := range errorsByAttempt {
		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("completion results: succeeded=%d failed=%d", succeeded, failed)
	}
	var rounds, batches int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM feed_rounds WHERE plan_id='plan-race'").Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM manure_batches WHERE source_round_id IN (SELECT id FROM feed_rounds WHERE plan_id='plan-race')").Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if rounds != 1 || batches != 1 {
		t.Fatalf("rounds=%d batches=%d", rounds, batches)
	}
}

func TestPlanListFiltersAndPaginatesInStableOrder(t *testing.T) {
	db := bootstrapDatabase(t)
	for i := 0; i < 5; i++ {
		scheduledPlan(t, db, fmt.Sprintf("plan-page-%d", i), 10)
	}
	repo := FeedRepository{DB: db}
	items, total, err := repo.ListPlans(context.Background(), "demo", "scheduled", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(items) != 2 {
		t.Fatalf("total=%d len=%d", total, len(items))
	}
	if items[0].ID >= items[1].ID {
		t.Fatalf("items are not in stable order: %s then %s", items[0].ID, items[1].ID)
	}
	empty, total, err := repo.ListPlans(context.Background(), "demo", "completed", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(empty) != 0 {
		t.Fatalf("completed results total=%d items=%v", total, empty)
	}
}

func TestManureInspectionAndApprovalEnforceVersionAndCreateCompost(t *testing.T) {
	db := bootstrapDatabase(t)
	scheduledPlan(t, db, "plan-manure", 100)
	input := completionInput("plan-manure", "manure")
	_, batch, err := (FeedRepository{DB: db}).Complete(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	repo := ManureRepository{DB: db}
	inspected, err := repo.Inspect(context.Background(), manure.InspectInput{
		TenantID:        "demo",
		BatchID:         batch.ID,
		ActorID:         "env-1",
		RequestID:       "req-inspect",
		AuditID:         "audit-inspect",
		ExpectedVersion: 1,
		At:              time.Now().UTC(),
	})
	if err != nil || inspected.Status != "inspected" || inspected.Version != 2 {
		t.Fatalf("Inspect() batch=%+v err=%v", inspected, err)
	}
	if _, _, err := repo.Approve(context.Background(), manure.ApproveInput{
		TenantID:        "demo",
		BatchID:         batch.ID,
		ActorID:         "env-1",
		LotID:           "compost-stale",
		RequestID:       "req-stale",
		AuditID:         "audit-stale",
		OutboxID:        "job-stale",
		ExpectedVersion: 1,
		Moisture:        0.35,
		At:              time.Now().UTC(),
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale Approve() error = %v", err)
	}
	approved, lot, err := repo.Approve(context.Background(), manure.ApproveInput{
		TenantID:        "demo",
		BatchID:         batch.ID,
		ActorID:         "env-1",
		LotID:           "compost-ok",
		RequestID:       "req-approve",
		AuditID:         "audit-approve",
		OutboxID:        "job-approve",
		ExpectedVersion: 2,
		Moisture:        0.35,
		At:              time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.Version != 3 || lot.BatchID != batch.ID || lot.OutputKg <= 0 {
		t.Fatalf("approved=%+v lot=%+v", approved, lot)
	}
}

func TestInventoryAllocationUsesEarliestExpiryAndRollsBackShortage(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	repo := InventoryRepository{DB: db}
	now := time.Now().UTC()
	lots := []inventory.Lot{
		{ID: "lot-late", TenantID: "demo", FeedCode: "TMR-01", SupplierID: "supplier-a", QuantityKg: 80, ProducedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(72 * time.Hour), ReceivedAt: now.Add(-24 * time.Hour), Status: inventory.LotReleased, Version: 1},
		{ID: "lot-early", TenantID: "demo", FeedCode: "TMR-01", SupplierID: "supplier-b", QuantityKg: 50, ProducedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(24 * time.Hour), ReceivedAt: now.Add(-12 * time.Hour), Status: inventory.LotReleased, Version: 1},
	}
	for _, lot := range lots {
		if err := repo.Receive(ctx, lot); err != nil {
			t.Fatalf("Receive(%s): %v", lot.ID, err)
		}
	}
	scheduledPlan(t, db, "plan-inventory", 70)
	reservation, err := repo.Allocate(ctx, "demo", "plan-inventory", "TMR-01", 70, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(reservation.Lines) != 2 || reservation.Lines[0].LotID != "lot-early" || reservation.Lines[0].Kg != 50 {
		t.Fatalf("reservation lines = %+v", reservation.Lines)
	}
	before, err := repo.Lots(ctx, "demo", "TMR-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Allocate(ctx, "demo", "plan-shortage", "TMR-01", 500, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("shortage Allocate() error = %v", err)
	}
	after, err := repo.Lots(ctx, "demo", "TMR-01")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("lots changed after shortage: before=%+v after=%+v", before, after)
	}
}

func TestAuditQueryCombinesFiltersTimeRangeAndPagination(t *testing.T) {
	db := bootstrapDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, action := range []string{"feed.schedule", "feed.complete", "feed.complete", "compost.approve"} {
		_, err := db.Pool.Exec(ctx, `INSERT INTO audit_events(id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id,details,created_at) VALUES($1,'demo',$2,$3,$4,$5,'success',$6,$7,$8)`,
			fmt.Sprintf("audit-filter-%d", i),
			[]string{"mgr-1", "op-1", "op-1", "env-1"}[i],
			action,
			[]string{"feed_plan", "feed_plan", "feed_plan", "manure_batch"}[i],
			fmt.Sprintf("object-%d", i),
			fmt.Sprintf("request-%d", i),
			fmt.Sprintf(`{"sequence":%d}`, i),
			now.Add(time.Duration(i)*time.Minute),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := db.ListAudit(ctx, AuditFilter{
		TenantID:   "demo",
		ActorID:    "op-1",
		Action:     "feed.complete",
		ObjectType: "feed_plan",
		Outcome:    "success",
		From:       now,
		Until:      now.Add(10 * time.Minute),
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 1 || items[0].ID != "audit-filter-2" {
		t.Fatalf("total=%d items=%+v", total, items)
	}
	if items[0].Details["sequence"] != float64(2) {
		t.Fatalf("details = %#v", items[0].Details)
	}
	if _, _, err := db.ListAudit(ctx, AuditFilter{TenantID: "demo", From: now, Until: now, Limit: 10}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid time range error = %v", err)
	}
}
