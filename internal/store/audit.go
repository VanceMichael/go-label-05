package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go-base/internal/domain"
)

type AuditFilter struct {
	TenantID   string
	ActorID    string
	Action     string
	ObjectType string
	ObjectID   string
	Outcome    string
	From       time.Time
	Until      time.Time
	Offset     int
	Limit      int
}

type AuditRecord struct {
	ID         string
	TenantID   string
	ActorID    string
	Action     string
	ObjectType string
	ObjectID   string
	Outcome    string
	RequestID  string
	Details    map[string]any
	CreatedAt  time.Time
}

func (d *Database) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditRecord, int, error) {
	if filter.TenantID == "" {
		return nil, 0, fmt.Errorf("%w: audit tenant", domain.ErrInvalid)
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	clauses := []string{"tenant_id=$1"}
	args := []any{filter.TenantID}
	add := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s=$%d", column, len(args)))
	}
	add("actor_id", filter.ActorID)
	add("action", filter.Action)
	add("object_type", filter.ObjectType)
	add("object_id", filter.ObjectID)
	add("outcome", filter.Outcome)
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		clauses = append(clauses, fmt.Sprintf("created_at>=$%d", len(args)))
	}
	if !filter.Until.IsZero() {
		if !filter.From.IsZero() && !filter.Until.After(filter.From) {
			return nil, 0, fmt.Errorf("%w: audit time range", domain.ErrInvalid)
		}
		args = append(args, filter.Until)
		clauses = append(clauses, fmt.Sprintf("created_at<$%d", len(args)))
	}
	where := joinClauses(clauses)
	var total int
	if err := d.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, filter.Limit, filter.Offset)
	query := fmt.Sprintf(`SELECT id,tenant_id,actor_id,action,object_type,object_id,outcome,request_id,details,created_at FROM audit_events WHERE %s ORDER BY created_at DESC,id DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
	rows, err := d.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]AuditRecord, 0, filter.Limit)
	for rows.Next() {
		var record AuditRecord
		var raw []byte
		if err := rows.Scan(&record.ID, &record.TenantID, &record.ActorID, &record.Action, &record.ObjectType, &record.ObjectID, &record.Outcome, &record.RequestID, &raw, &record.CreatedAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal(raw, &record.Details); err != nil {
			return nil, 0, fmt.Errorf("decode audit %s: %w", record.ID, err)
		}
		items = append(items, record)
	}
	return items, total, rows.Err()
}

func joinClauses(clauses []string) string {
	result := ""
	for index, clause := range clauses {
		if index > 0 {
			result += " AND "
		}
		result += clause
	}
	return result
}
