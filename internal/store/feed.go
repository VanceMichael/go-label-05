package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"go-base/internal/domain"
	"go-base/internal/feed"
	"time"
)

type FeedRepository struct{ DB *Database }

func (r FeedRepository) Schedule(ctx context.Context, in feed.ScheduleInput) (domain.FeedPlan, error) {
	p := in.Plan
	err := r.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var role domain.Role
		if err := tx.QueryRow(ctx, "SELECT role FROM users WHERE tenant_id=$1 AND id=$2 AND disabled=false", p.TenantID, p.OperatorID).Scan(&role); err != nil {
			return mapNotFound(err, "operator")
		}
		if role != domain.RoleOperator {
			return fmt.Errorf("%w: assigned user is not operator", domain.ErrInvalid)
		}
		var reserved float64
		tag, err := tx.Exec(ctx, `UPDATE feed_inventory SET available_kg=available_kg-$3,reserved_kg=reserved_kg+$3,version=version+1,updated_at=now() WHERE tenant_id=$1 AND feed_code=$2 AND available_kg >= $3`, p.TenantID, in.FeedCode, p.FeedKg)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: insufficient feed", domain.ErrConflict)
		}
		reserved = p.FeedKg
		if _, err = tx.Exec(ctx, `INSERT INTO feed_plans(id,tenant_id,group_id,operator_id,feed_code,feed_kg,scheduled_for,status,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.ID, p.TenantID, p.GroupID, p.OperatorID, in.FeedCode, p.FeedKg, p.ScheduledFor, p.Status, p.Version); err != nil {
			return fmt.Errorf("insert plan after reserve %.3f: %w", reserved, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id) VALUES($1,$2,$3,'feed.schedule','feed_plan',$4,'success',$5)`, in.AuditID, p.TenantID, in.ActorID, p.ID, in.RequestID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"plan_id": p.ID, "scheduled_for": p.ScheduledFor})
		_, err = tx.Exec(ctx, `INSERT INTO outbox_jobs(id,tenant_id,topic,payload,status,available_at) VALUES($1,$2,'feed.plan.scheduled',$3,'pending',now())`, in.OutboxID, p.TenantID, payload)
		return err
	})
	return p, err
}
func (r FeedRepository) Complete(ctx context.Context, in feed.CompleteInput) (domain.FeedRound, domain.ManureBatch, error) {
	round := domain.FeedRound{ID: in.RoundID, TenantID: in.TenantID, PlanID: in.PlanID, IdempotencyKey: in.IdempotencyKey, Status: "recorded", DeliveredKg: in.DeliveredKg, RecordedAt: in.At}
	batch := domain.ManureBatch{ID: in.BatchID, TenantID: in.TenantID, SourceRoundID: in.RoundID, Status: "collected", WeightKg: in.ManureKg, CollectedAt: in.At, Version: 1}
	err := r.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var existingPlan string
		var existingDelivered float64
		existingErr := tx.QueryRow(ctx, `SELECT plan_id,delivered_kg FROM feed_rounds WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&existingPlan, &existingDelivered)
		if existingErr == nil {
			if existingPlan != in.PlanID || existingDelivered != in.DeliveredKg {
				return fmt.Errorf("%w: idempotency key reused with different request", domain.ErrConflict)
			}
			if err := tx.QueryRow(ctx, `SELECT id,tenant_id,plan_id,idempotency_key,status,delivered_kg,recorded_at FROM feed_rounds WHERE tenant_id=$1 AND idempotency_key=$2`, in.TenantID, in.IdempotencyKey).Scan(&round.ID, &round.TenantID, &round.PlanID, &round.IdempotencyKey, &round.Status, &round.DeliveredKg, &round.RecordedAt); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT id,tenant_id,group_id,source_round_id,status,weight_kg,collected_at,version FROM manure_batches WHERE tenant_id=$1 AND source_round_id=$2`, in.TenantID, round.ID).Scan(&batch.ID, &batch.TenantID, &batch.GroupID, &batch.SourceRoundID, &batch.Status, &batch.WeightKg, &batch.CollectedAt, &batch.Version)
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return existingErr
		}
		var status, groupID, feedCode string
		var feedKg float64
		var version int64
		err := tx.QueryRow(ctx, `SELECT status,group_id,feed_code,feed_kg,version FROM feed_plans WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, in.TenantID, in.PlanID).Scan(&status, &groupID, &feedCode, &feedKg, &version)
		if err != nil {
			return mapNotFound(err, "plan")
		}
		if status != "scheduled" {
			return fmt.Errorf("%w: plan is %s", domain.ErrConflict, status)
		}
		if version != in.ExpectedVersion {
			return fmt.Errorf("%w: expected version %d got %d", domain.ErrConflict, in.ExpectedVersion, version)
		}
		if in.DeliveredKg > feedKg*1.05 {
			return fmt.Errorf("%w: delivery exceeds tolerance", domain.ErrInvalid)
		}
		batch.GroupID = groupID
		tag, err := tx.Exec(ctx, `UPDATE feed_plans SET status='completed',completed_at=$3,version=version+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND version=$4`, in.TenantID, in.PlanID, in.At, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO feed_rounds(id,tenant_id,plan_id,idempotency_key,delivered_kg,status,recorded_at) VALUES($1,$2,$3,$4,$5,'recorded',$6)`, round.ID, round.TenantID, round.PlanID, round.IdempotencyKey, round.DeliveredKg, round.RecordedAt); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE feed_inventory SET reserved_kg=reserved_kg-$3,version=version+1,updated_at=$4 WHERE tenant_id=$1 AND feed_code=$2 AND reserved_kg >= $3`, in.TenantID, feedCode, feedKg, in.At); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO manure_batches(id,tenant_id,group_id,source_round_id,weight_kg,collected_at,status,version) VALUES($1,$2,$3,$4,$5,$6,'collected',1)`, batch.ID, batch.TenantID, batch.GroupID, batch.SourceRoundID, batch.WeightKg, batch.CollectedAt); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id) VALUES($1,$2,$3,'feed.complete','feed_plan',$4,'success',$5)`, in.AuditID, in.TenantID, in.ActorID, in.PlanID, in.RequestID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"plan_id": in.PlanID, "batch_id": batch.ID})
		_, err = tx.Exec(ctx, `INSERT INTO outbox_jobs(id,tenant_id,topic,payload,status,available_at) VALUES($1,$2,'manure.batch.collected',$3,'pending',$4)`, in.OutboxID, in.TenantID, payload, in.At)
		return err
	})
	return round, batch, err
}
func (r FeedRepository) GetPlan(ctx context.Context, tenant, id string) (domain.FeedPlan, error) {
	var p domain.FeedPlan
	err := r.DB.Pool.QueryRow(ctx, `SELECT id,tenant_id,group_id,operator_id,status,feed_kg,scheduled_for,completed_at,version FROM feed_plans WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&p.ID, &p.TenantID, &p.GroupID, &p.OperatorID, &p.Status, &p.FeedKg, &p.ScheduledFor, &p.CompletedAt, &p.Version)
	return p, mapNotFound(err, "plan")
}
func (r FeedRepository) ListPlans(ctx context.Context, tenant, status string, offset, limit int) ([]domain.FeedPlan, int, error) {
	where := "tenant_id=$1"
	args := []any{tenant}
	if status != "" {
		where += " AND status=$2"
		args = append(args, status)
	}
	var total int
	if err := r.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM feed_plans WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := r.DB.Pool.Query(ctx, fmt.Sprintf("SELECT id,tenant_id,group_id,operator_id,status,feed_kg,scheduled_for,completed_at,version FROM feed_plans WHERE %s ORDER BY scheduled_for,id LIMIT $%d OFFSET $%d", where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []domain.FeedPlan{}
	for rows.Next() {
		var p domain.FeedPlan
		if err = rows.Scan(&p.ID, &p.TenantID, &p.GroupID, &p.OperatorID, &p.Status, &p.FeedKg, &p.ScheduledFor, &p.CompletedAt, &p.Version); err != nil {
			return nil, 0, err
		}
		items = append(items, p)
	}
	return items, total, rows.Err()
}
func mapNotFound(err error, object string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, object)
	}
	return err
}

var _ = time.Time{}
