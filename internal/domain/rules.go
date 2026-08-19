package domain

import (
	"fmt"
	"strings"
	"time"
)

func ValidateRole(role Role) error {
	switch role {
	case RoleManager, RoleOperator, RoleEnvironment:
		return nil
	}
	return fmt.Errorf("%w: role %q", ErrInvalid, role)
}

func CanPlan(role Role) bool           { return role == RoleManager || role == RoleOperator }
func CanApproveCompost(role Role) bool { return role == RoleEnvironment || role == RoleManager }
func NormalizeEmail(v string) string   { return strings.ToLower(strings.TrimSpace(v)) }

func ValidateFeedPlan(p FeedPlan, now time.Time) error {
	if p.TenantID == "" || p.GroupID == "" || p.OperatorID == "" || p.FeedKg <= 0 {
		return fmt.Errorf("%w: incomplete feed plan", ErrInvalid)
	}
	if p.ScheduledFor.Before(now.Add(-10 * time.Minute)) {
		return fmt.Errorf("%w: schedule is in the past", ErrInvalid)
	}
	return nil
}

func ValidateTransition(current, next string) error {
	allowed := map[string]map[string]bool{"draft": {"scheduled": true}, "scheduled": {"completed": true, "cancelled": true}, "completed": {}, "cancelled": {}, "collected": {"inspected": true}, "inspected": {"composting": true, "rejected": true}, "composting": {"approved": true}}
	if !allowed[current][next] {
		return fmt.Errorf("%w: cannot move %s to %s", ErrConflict, current, next)
	}
	return nil
}

func CompostOutput(weight, moisture float64) (float64, error) {
	if weight <= 0 || moisture < 0 || moisture >= 1 {
		return 0, fmt.Errorf("%w: invalid manure measurement", ErrInvalid)
	}
	return weight * (1 - moisture) * 0.72, nil
}
