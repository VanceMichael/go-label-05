package manure

import (
	"context"
	"fmt"
	"go-base/internal/auth"
	"go-base/internal/domain"
	"go-base/internal/identity"
	"time"
)

type Service struct {
	Repo Repository
	Now  func() time.Time
}

func (s Service) Inspect(ctx context.Context, user domain.User, batchID, requestID string, version int64) (domain.ManureBatch, error) {
	if err := auth.RequireRole(user, domain.RoleEnvironment); err != nil {
		return domain.ManureBatch{}, err
	}
	if batchID == "" || version < 1 {
		return domain.ManureBatch{}, fmt.Errorf("%w: inspection fields", domain.ErrInvalid)
	}
	id, err := identity.New("audit")
	if err != nil {
		return domain.ManureBatch{}, err
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.Repo.Inspect(ctx, InspectInput{TenantID: user.TenantID, BatchID: batchID, ActorID: user.ID, RequestID: requestID, AuditID: id, ExpectedVersion: version, At: now})
}
func (s Service) Approve(ctx context.Context, user domain.User, batchID, requestID string, version int64, moisture float64) (domain.ManureBatch, domain.CompostLot, error) {
	if err := auth.RequireRole(user, domain.RoleEnvironment, domain.RoleManager); err != nil {
		return domain.ManureBatch{}, domain.CompostLot{}, err
	}
	if batchID == "" || version < 1 {
		return domain.ManureBatch{}, domain.CompostLot{}, fmt.Errorf("%w: approval fields", domain.ErrInvalid)
	}
	ids := make([]string, 3)
	for i, p := range []string{"compost", "audit", "job"} {
		id, err := identity.New(p)
		if err != nil {
			return domain.ManureBatch{}, domain.CompostLot{}, err
		}
		ids[i] = id
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.Repo.Approve(ctx, ApproveInput{TenantID: user.TenantID, BatchID: batchID, ActorID: user.ID, LotID: ids[0], RequestID: requestID, AuditID: ids[1], OutboxID: ids[2], ExpectedVersion: version, Moisture: moisture, At: now})
}
func (s Service) List(ctx context.Context, user domain.User, status string, page, size int) ([]domain.ManureBatch, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	return s.Repo.List(ctx, user.TenantID, status, (page-1)*size, size)
}
