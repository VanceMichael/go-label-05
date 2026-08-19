package feed

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

func (s Service) Schedule(ctx context.Context, user domain.User, groupID, operatorID, feedCode string, feedKg float64, scheduled time.Time, requestID string) (domain.FeedPlan, error) {
	if err := auth.RequireRole(user, domain.RoleManager); err != nil {
		return domain.FeedPlan{}, err
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	planID, err := identity.New("plan")
	if err != nil {
		return domain.FeedPlan{}, err
	}
	auditID, err := identity.New("audit")
	if err != nil {
		return domain.FeedPlan{}, err
	}
	outboxID, err := identity.New("job")
	if err != nil {
		return domain.FeedPlan{}, err
	}
	p := domain.FeedPlan{ID: planID, TenantID: user.TenantID, GroupID: groupID, OperatorID: operatorID, Status: "scheduled", FeedKg: feedKg, ScheduledFor: scheduled, Version: 1}
	if err = domain.ValidateFeedPlan(p, now); err != nil {
		return domain.FeedPlan{}, err
	}
	return s.Repo.Schedule(ctx, ScheduleInput{Plan: p, FeedCode: feedCode, ActorID: user.ID, RequestID: requestID, AuditID: auditID, OutboxID: outboxID})
}
func (s Service) Complete(ctx context.Context, user domain.User, planID, key string, delivered, manure float64, version int64, requestID string) (domain.FeedRound, domain.ManureBatch, error) {
	if err := auth.RequireRole(user, domain.RoleOperator, domain.RoleManager); err != nil {
		return domain.FeedRound{}, domain.ManureBatch{}, err
	}
	if planID == "" || key == "" || delivered <= 0 || manure <= 0 {
		return domain.FeedRound{}, domain.ManureBatch{}, fmt.Errorf("%w: completion fields", domain.ErrInvalid)
	}
	now := s.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ids := make([]string, 4)
	prefixes := []string{"round", "manure", "audit", "job"}
	for i, p := range prefixes {
		id, err := identity.New(p)
		if err != nil {
			return domain.FeedRound{}, domain.ManureBatch{}, err
		}
		ids[i] = id
	}
	return s.Repo.Complete(ctx, CompleteInput{TenantID: user.TenantID, PlanID: planID, ActorID: user.ID, RoundID: ids[0], BatchID: ids[1], IdempotencyKey: key, RequestID: requestID, AuditID: ids[2], OutboxID: ids[3], DeliveredKg: delivered, ManureKg: manure, At: now, ExpectedVersion: version})
}
func (s Service) List(ctx context.Context, user domain.User, status string, page, size int) ([]domain.FeedPlan, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	return s.Repo.ListPlans(ctx, user.TenantID, status, (page-1)*size, size)
}
