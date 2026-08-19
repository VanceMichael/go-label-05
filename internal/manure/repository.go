package manure

import (
	"context"
	"go-base/internal/domain"
	"time"
)

type InspectInput struct {
	TenantID, BatchID, ActorID, RequestID, AuditID string
	ExpectedVersion                                int64
	At                                             time.Time
}
type ApproveInput struct {
	TenantID, BatchID, ActorID, LotID, RequestID, AuditID, OutboxID string
	ExpectedVersion                                                 int64
	Moisture                                                        float64
	At                                                              time.Time
}
type Repository interface {
	Inspect(context.Context, InspectInput) (domain.ManureBatch, error)
	Approve(context.Context, ApproveInput) (domain.ManureBatch, domain.CompostLot, error)
	List(context.Context, string, string, int, int) ([]domain.ManureBatch, int, error)
}
