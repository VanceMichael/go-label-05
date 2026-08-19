package report

import (
	"context"
	"fmt"
	"time"

	"go-base/internal/domain"
	"go-base/internal/store"
)

type Operations struct {
	TenantID          string
	GeneratedAt       time.Time
	ActiveGroups      int
	ScheduledPlans    int
	CompletedPlans    int
	FeedAvailableKg   float64
	FeedReservedKg    float64
	CollectedManureKg float64
	ApprovedCompostKg float64
	PendingJobs       int
	DeadJobs          int
	ActiveSessions    int
}

type StatusCount struct {
	Status string
	Count  int
}

type TimelinePoint struct {
	Bucket time.Time
	Count  int
	Total  float64
}

type Builder struct{ DB *store.Database }

func (builder Builder) Snapshot(ctx context.Context, tenant string, now time.Time) (Operations, error) {
	if tenant == "" {
		return Operations{}, fmt.Errorf("%w: report tenant", domain.ErrInvalid)
	}
	result := Operations{TenantID: tenant, GeneratedAt: now}
	queries := []struct {
		query string
		args  []any
		dest  any
	}{
		{`SELECT count(*) FROM animal_groups WHERE tenant_id=$1 AND status='active'`, []any{tenant}, &result.ActiveGroups},
		{`SELECT count(*) FROM feed_plans WHERE tenant_id=$1 AND status='scheduled'`, []any{tenant}, &result.ScheduledPlans},
		{`SELECT count(*) FROM feed_plans WHERE tenant_id=$1 AND status='completed'`, []any{tenant}, &result.CompletedPlans},
		{`SELECT COALESCE(sum(available_kg),0) FROM feed_inventory WHERE tenant_id=$1`, []any{tenant}, &result.FeedAvailableKg},
		{`SELECT COALESCE(sum(reserved_kg),0) FROM feed_inventory WHERE tenant_id=$1`, []any{tenant}, &result.FeedReservedKg},
		{`SELECT COALESCE(sum(weight_kg),0) FROM manure_batches WHERE tenant_id=$1 AND status IN ('collected','inspected','composting')`, []any{tenant}, &result.CollectedManureKg},
		{`SELECT COALESCE(sum(output_kg),0) FROM compost_lots WHERE tenant_id=$1 AND status='approved'`, []any{tenant}, &result.ApprovedCompostKg},
		{`SELECT count(*) FROM outbox_jobs WHERE tenant_id=$1 AND status IN ('pending','retry','running')`, []any{tenant}, &result.PendingJobs},
		{`SELECT count(*) FROM outbox_jobs WHERE tenant_id=$1 AND status='dead'`, []any{tenant}, &result.DeadJobs},
		{`SELECT count(*) FROM sessions WHERE tenant_id=$1 AND revoked_at IS NULL AND expires_at>$2`, []any{tenant, now}, &result.ActiveSessions},
	}
	for _, query := range queries {
		if err := builder.DB.Pool.QueryRow(ctx, query.query, query.args...).Scan(query.dest); err != nil {
			return Operations{}, err
		}
	}
	return result, nil
}

func (builder Builder) PlanStatuses(ctx context.Context, tenant string) ([]StatusCount, error) {
	rows, err := builder.DB.Pool.Query(ctx, `SELECT status,count(*) FROM feed_plans WHERE tenant_id=$1 GROUP BY status ORDER BY status`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StatusCount{}
	for rows.Next() {
		var item StatusCount
		if err := rows.Scan(&item.Status, &item.Count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (builder Builder) FeedTimeline(ctx context.Context, tenant string, from, until time.Time, bucket time.Duration) ([]TimelinePoint, error) {
	if tenant == "" || !until.After(from) || bucket < time.Hour || bucket > 31*24*time.Hour {
		return nil, fmt.Errorf("%w: feed timeline range", domain.ErrInvalid)
	}
	rows, err := builder.DB.Pool.Query(ctx, `SELECT recorded_at,delivered_kg FROM feed_rounds WHERE tenant_id=$1 AND recorded_at>=$2 AND recorded_at<$3 ORDER BY recorded_at,id`, tenant, from, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byBucket := map[int64]*TimelinePoint{}
	for rows.Next() {
		var at time.Time
		var kg float64
		if err := rows.Scan(&at, &kg); err != nil {
			return nil, err
		}
		index := int64(at.Sub(from) / bucket)
		point := byBucket[index]
		if point == nil {
			point = &TimelinePoint{Bucket: from.Add(time.Duration(index) * bucket)}
			byBucket[index] = point
		}
		point.Count++
		point.Total += kg
	}
	result := make([]TimelinePoint, 0, int(until.Sub(from)/bucket)+1)
	for current := from; current.Before(until); current = current.Add(bucket) {
		index := int64(current.Sub(from) / bucket)
		if point := byBucket[index]; point != nil {
			result = append(result, *point)
		} else {
			result = append(result, TimelinePoint{Bucket: current})
		}
	}
	return result, rows.Err()
}
