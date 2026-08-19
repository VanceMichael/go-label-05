package feed

import (
	"context"
	"go-base/internal/domain"
	"time"
)

type ScheduleInput struct {
	Plan                                            domain.FeedPlan
	FeedCode, ActorID, RequestID, AuditID, OutboxID string
}
type CompleteInput struct {
	TenantID, PlanID, ActorID, RoundID, BatchID, IdempotencyKey, RequestID, AuditID, OutboxID string
	DeliveredKg, ManureKg                                                                     float64
	At                                                                                        time.Time
	ExpectedVersion                                                                           int64
}
type Repository interface {
	Schedule(context.Context, ScheduleInput) (domain.FeedPlan, error)
	Complete(context.Context, CompleteInput) (domain.FeedRound, domain.ManureBatch, error)
	GetPlan(context.Context, string, string) (domain.FeedPlan, error)
	ListPlans(context.Context, string, string, int, int) ([]domain.FeedPlan, int, error)
}
