package worker

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
	"go-base/internal/store"
)

var workerDatabaseSequence atomic.Uint64

func workerTestDatabase(t *testing.T) *store.Database {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = "postgres://farm:farm@127.0.0.1:55432/farm?sslmode=disable"
	}
	adminConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("herdcycle_worker_%d_%d", time.Now().UnixNano(), workerDatabaseSequence.Add(1))
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	appURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	appURL.Path = "/" + name
	db, err := store.Open(ctx, appURL.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop Worker test database: %v", err)
		}
		admin.Close()
	})
	return db
}

func insertJob(t *testing.T, db *store.Database, id, status string, attempts int, available, lockedAt time.Time, lockedBy string) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(), `
		INSERT INTO outbox_jobs(id,tenant_id,topic,payload,status,attempts,available_at,locked_at,locked_by)
		VALUES($1,'demo','feed.plan.scheduled',$2,$3,$4,$5,$6,$7)`,
		id,
		fmt.Sprintf(`{"job_id":%q}`, id),
		status,
		attempts,
		available,
		nullableTime(lockedAt),
		lockedBy,
	)
	if err != nil {
		t.Fatalf("insert job %s: %v", id, err)
	}
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

type recordingPublisher struct {
	mu       sync.Mutex
	calls    map[string]int
	err      error
	entered  chan string
	release  <-chan struct{}
	contexts []error
}

func (publisher *recordingPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if publisher.entered != nil {
		publisher.entered <- string(payload)
	}
	if publisher.release != nil {
		select {
		case <-publisher.release:
		case <-ctx.Done():
			publisher.mu.Lock()
			publisher.contexts = append(publisher.contexts, ctx.Err())
			publisher.mu.Unlock()
			return ctx.Err()
		}
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.calls == nil {
		publisher.calls = map[string]int{}
	}
	publisher.calls[string(payload)]++
	publisher.contexts = append(publisher.contexts, ctx.Err())
	return publisher.err
}

func (publisher *recordingPublisher) totalCalls() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	var total int
	for _, count := range publisher.calls {
		total += count
	}
	return total
}

func jobState(t *testing.T, db *store.Database, id string) (string, int, time.Time, string, *time.Time) {
	t.Helper()
	var status, lastError string
	var attempts int
	var available time.Time
	var lockedAt *time.Time
	if err := db.Pool.QueryRow(context.Background(), `SELECT status,attempts,available_at,last_error,locked_at FROM outbox_jobs WHERE id=$1`, id).Scan(&status, &attempts, &available, &lastError, &lockedAt); err != nil {
		t.Fatal(err)
	}
	return status, attempts, available, lastError, lockedAt
}

func TestRunOnceDeliversDueJobsInOrderAndLeavesFutureJobsPending(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	insertJob(t, db, "job-b", "pending", 0, now.Add(-time.Second), time.Time{}, "")
	insertJob(t, db, "job-a", "pending", 0, now.Add(-time.Second), time.Time{}, "")
	insertJob(t, db, "job-future", "pending", 0, now.Add(time.Hour), time.Time{}, "")
	publisher := &recordingPublisher{}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "worker-order", Batch: 10, MaxAttempts: 3, Now: func() time.Time { return now }}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.totalCalls() != 2 {
		t.Fatalf("publish calls = %d", publisher.totalCalls())
	}
	for _, id := range []string{"job-a", "job-b"} {
		status, attempts, _, lastError, lockedAt := jobState(t, db, id)
		if status != "delivered" || attempts != 1 || lastError != "" || lockedAt != nil {
			t.Errorf("%s status=%s attempts=%d lastError=%q lockedAt=%v", id, status, attempts, lastError, lockedAt)
		}
	}
	status, attempts, _, _, _ := jobState(t, db, "job-future")
	if status != "pending" || attempts != 0 {
		t.Fatalf("future job status=%s attempts=%d", status, attempts)
	}
}

func TestConcurrentWorkersDoNotPublishTheSameClaim(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Now().UTC()
	insertJob(t, db, "job-shared", "pending", 0, now.Add(-time.Minute), time.Time{}, "")
	release := make(chan struct{})
	entered := make(chan string, 2)
	publisher := &recordingPublisher{entered: entered, release: release}
	workers := []Outbox{
		{DB: db, Publisher: publisher, WorkerID: "worker-a", Batch: 1, MaxAttempts: 3, ClaimTimeout: time.Minute, Now: func() time.Time { return now }},
		{DB: db, Publisher: publisher, WorkerID: "worker-b", Batch: 1, MaxAttempts: 3, ClaimTimeout: time.Minute, Now: func() time.Time { return now }},
	}
	result := make(chan error, len(workers))
	go func() { result <- workers[0].RunOnce(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker did not begin publishing")
	}
	go func() { result <- workers[1].RunOnce(context.Background()) }()
	select {
	case duplicate := <-entered:
		t.Fatalf("second worker published claimed job: %s", duplicate)
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	for range workers {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if publisher.totalCalls() != 1 {
		t.Fatalf("publish calls = %d", publisher.totalCalls())
	}
	status, attempts, _, _, _ := jobState(t, db, "job-shared")
	if status != "delivered" || attempts != 1 {
		t.Fatalf("job status=%s attempts=%d", status, attempts)
	}
}

func TestPublishFailureSchedulesRetryWithBackoff(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	insertJob(t, db, "job-retry", "pending", 0, now, time.Time{}, "")
	publisher := &recordingPublisher{err: errors.New("broker unavailable")}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "worker-retry", Batch: 1, MaxAttempts: 3, Now: func() time.Time { return now }}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, available, lastError, lockedAt := jobState(t, db, "job-retry")
	if status != "retry" || attempts != 1 || !available.Equal(now.Add(time.Second)) {
		t.Fatalf("status=%s attempts=%d available=%v", status, attempts, available)
	}
	if lastError != "broker unavailable" || lockedAt != nil {
		t.Fatalf("lastError=%q lockedAt=%v", lastError, lockedAt)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.totalCalls() != 1 {
		t.Fatalf("job retried before available_at: calls=%d", publisher.totalCalls())
	}
}

func TestLastAllowedFailureMovesJobToDead(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Now().UTC()
	insertJob(t, db, "job-dead", "retry", 2, now.Add(-time.Minute), time.Time{}, "")
	publisher := &recordingPublisher{err: errors.New("permanent payload rejection")}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "worker-dead", Batch: 1, MaxAttempts: 3, Now: func() time.Time { return now }}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, _, lastError, lockedAt := jobState(t, db, "job-dead")
	if status != "dead" || attempts != 3 || lastError != "permanent payload rejection" || lockedAt != nil {
		t.Fatalf("status=%s attempts=%d lastError=%q lockedAt=%v", status, attempts, lastError, lockedAt)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.totalCalls() != 1 {
		t.Fatalf("dead job published again: calls=%d", publisher.totalCalls())
	}
}

func TestStaleRunningClaimCanBeRecovered(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Now().UTC()
	insertJob(t, db, "job-stale", "running", 1, now.Add(-time.Hour), now.Add(-10*time.Minute), "crashed-worker")
	publisher := &recordingPublisher{}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "recovery-worker", Batch: 1, MaxAttempts: 4, ClaimTimeout: 2 * time.Minute, Now: func() time.Time { return now }}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, attempts, _, lastError, lockedAt := jobState(t, db, "job-stale")
	if status != "delivered" || attempts != 2 || lastError != "" || lockedAt != nil {
		t.Fatalf("status=%s attempts=%d lastError=%q lockedAt=%v", status, attempts, lastError, lockedAt)
	}
	if publisher.totalCalls() != 1 {
		t.Fatalf("publish calls = %d", publisher.totalCalls())
	}
}

func TestFreshRunningClaimIsNotStolen(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Now().UTC()
	insertJob(t, db, "job-fresh", "running", 0, now.Add(-time.Hour), now.Add(-time.Second), "active-worker")
	publisher := &recordingPublisher{}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "other-worker", Batch: 1, MaxAttempts: 3, ClaimTimeout: time.Minute, Now: func() time.Time { return now }}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.totalCalls() != 0 {
		t.Fatalf("fresh claim was stolen: calls=%d", publisher.totalCalls())
	}
	status, attempts, _, _, lockedAt := jobState(t, db, "job-fresh")
	if status != "running" || attempts != 0 || lockedAt == nil {
		t.Fatalf("status=%s attempts=%d lockedAt=%v", status, attempts, lockedAt)
	}
}

func TestCanceledPublishStillReleasesClaimAsRetry(t *testing.T) {
	db := workerTestDatabase(t)
	now := time.Now().UTC()
	insertJob(t, db, "job-canceled", "pending", 0, now.Add(-time.Second), time.Time{}, "")
	release := make(chan struct{})
	entered := make(chan string, 1)
	publisher := &recordingPublisher{entered: entered, release: release}
	worker := Outbox{DB: db, Publisher: publisher, WorkerID: "worker-canceled", Batch: 1, MaxAttempts: 3, Now: func() time.Time { return now }}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- worker.RunOnce(ctx) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher was not called")
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	status, attempts, _, lastError, lockedAt := jobState(t, db, "job-canceled")
	if status != "retry" || attempts != 1 || !errors.Is(context.Canceled, context.Canceled) {
		t.Fatalf("status=%s attempts=%d", status, attempts)
	}
	if !errors.Is(errors.New(lastError), context.Canceled) && lastError != context.Canceled.Error() {
		t.Fatalf("lastError=%q", lastError)
	}
	if lockedAt != nil {
		t.Fatalf("canceled job retained lock at %v", lockedAt)
	}
}

func TestRunStopsAfterContextCancellation(t *testing.T) {
	db := workerTestDatabase(t)
	publisher := &recordingPublisher{}
	worker := Outbox{DB: db, Publisher: publisher, Interval: 5 * time.Millisecond, WorkerID: "worker-loop"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunOnceRejectsIncompleteConfiguration(t *testing.T) {
	if err := (Outbox{}).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce accepted missing database and publisher")
	}
}
