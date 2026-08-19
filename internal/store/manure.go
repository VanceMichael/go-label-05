package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"go-base/internal/domain"
	"go-base/internal/manure"
)

type ManureRepository struct{ DB *Database }

func (r ManureRepository) Inspect(ctx context.Context, in manure.InspectInput) (domain.ManureBatch, error) {
	var b domain.ManureBatch
	err := r.DB.WithTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id,tenant_id,group_id,source_round_id,status,weight_kg,collected_at,version FROM manure_batches WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, in.TenantID, in.BatchID).Scan(&b.ID, &b.TenantID, &b.GroupID, &b.SourceRoundID, &b.Status, &b.WeightKg, &b.CollectedAt, &b.Version)
		if err != nil {
			return mapNotFound(err, "manure batch")
		}
		if err = domain.ValidateTransition(b.Status, "inspected"); err != nil {
			return err
		}
		if b.Version != in.ExpectedVersion {
			return domain.ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE manure_batches SET status='inspected',version=version+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND version=$4`, in.TenantID, in.BatchID, in.At, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		b.Status = "inspected"
		b.Version++
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id) VALUES($1,$2,$3,'manure.inspect','manure_batch',$4,'success',$5)`, in.AuditID, in.TenantID, in.ActorID, in.BatchID, in.RequestID)
		return err
	})
	return b, err
}
func (r ManureRepository) Approve(ctx context.Context, in manure.ApproveInput) (domain.ManureBatch, domain.CompostLot, error) {
	var b domain.ManureBatch
	var lot domain.CompostLot
	err := r.DB.WithTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id,tenant_id,group_id,source_round_id,status,weight_kg,collected_at,version FROM manure_batches WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, in.TenantID, in.BatchID).Scan(&b.ID, &b.TenantID, &b.GroupID, &b.SourceRoundID, &b.Status, &b.WeightKg, &b.CollectedAt, &b.Version)
		if err != nil {
			return mapNotFound(err, "manure batch")
		}
		if b.Status != "inspected" && b.Status != "composting" {
			return fmt.Errorf("%w: batch is %s", domain.ErrConflict, b.Status)
		}
		if b.Version != in.ExpectedVersion {
			return domain.ErrConflict
		}
		output, err := domain.CompostOutput(b.WeightKg, in.Moisture)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE manure_batches SET status='approved',version=version+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND version=$4`, in.TenantID, in.BatchID, in.At, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrConflict
		}
		lot = domain.CompostLot{ID: in.LotID, TenantID: in.TenantID, BatchID: in.BatchID, Status: "approved", OutputKg: output, ApprovedBy: in.ActorID, Version: 1}
		if _, err = tx.Exec(ctx, `INSERT INTO compost_lots(id,tenant_id,batch_id,status,output_kg,approved_by,version) VALUES($1,$2,$3,$4,$5,$6,1)`, lot.ID, lot.TenantID, lot.BatchID, lot.Status, lot.OutputKg, lot.ApprovedBy); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id) VALUES($1,$2,$3,'compost.approve','manure_batch',$4,'success',$5)`, in.AuditID, in.TenantID, in.ActorID, in.BatchID, in.RequestID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"batch_id": in.BatchID, "compost_lot_id": lot.ID, "output_kg": lot.OutputKg})
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_jobs(id,tenant_id,topic,payload,status,available_at) VALUES($1,$2,'compost.lot.approved',$3,'pending',$4)`, in.OutboxID, in.TenantID, payload, in.At); err != nil {
			return err
		}
		b.Status = "approved"
		b.Version++
		return nil
	})
	return b, lot, err
}
func (r ManureRepository) List(ctx context.Context, tenant, status string, offset, limit int) ([]domain.ManureBatch, int, error) {
	where := "tenant_id=$1"
	args := []any{tenant}
	if status != "" {
		where += " AND status=$2"
		args = append(args, status)
	}
	var total int
	if err := r.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM manure_batches WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := r.DB.Pool.Query(ctx, fmt.Sprintf("SELECT id,tenant_id,group_id,source_round_id,status,weight_kg,collected_at,version FROM manure_batches WHERE %s ORDER BY collected_at DESC,id LIMIT $%d OFFSET $%d", where, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []domain.ManureBatch{}
	for rows.Next() {
		var b domain.ManureBatch
		if err = rows.Scan(&b.ID, &b.TenantID, &b.GroupID, &b.SourceRoundID, &b.Status, &b.WeightKg, &b.CollectedAt, &b.Version); err != nil {
			return nil, 0, err
		}
		items = append(items, b)
	}
	return items, total, rows.Err()
}
