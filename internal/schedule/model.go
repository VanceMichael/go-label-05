package schedule

import (
	"fmt"
	"sort"
	"time"

	"go-base/internal/domain"
)

type Window struct {
	StartsAt time.Time
	EndsAt   time.Time
}

type ResourceKind string

const (
	ResourceOperator ResourceKind = "operator"
	ResourceFeeder   ResourceKind = "feeder"
	ResourceBarn     ResourceKind = "barn"
)

type Assignment struct {
	ID           string
	TenantID     string
	PlanID       string
	ResourceKind ResourceKind
	ResourceID   string
	Window       Window
	Priority     int
	Status       string
	Version      int64
}

type Constraint struct {
	ResourceKind ResourceKind
	ResourceID   string
	Unavailable  []Window
	DailyLimit   time.Duration
	Cooldown     time.Duration
}

type PlanRequest struct {
	ID            string
	TenantID      string
	BarnID        string
	OperatorIDs   []string
	FeederIDs     []string
	EarliestStart time.Time
	LatestEnd     time.Time
	Duration      time.Duration
	Priority      int
}

type Allocation struct {
	Request     PlanRequest
	Assignments []Assignment
	Scheduled   bool
	Reason      string
}

func (w Window) Validate() error {
	if w.StartsAt.IsZero() || w.EndsAt.IsZero() || !w.EndsAt.After(w.StartsAt) {
		return fmt.Errorf("%w: scheduling window", domain.ErrInvalid)
	}
	return nil
}

func (w Window) Duration() time.Duration {
	if !w.EndsAt.After(w.StartsAt) {
		return 0
	}
	return w.EndsAt.Sub(w.StartsAt)
}

func Overlaps(a, b Window) bool {
	return a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt)
}

func Touches(a, b Window) bool {
	return a.EndsAt.Equal(b.StartsAt) || b.EndsAt.Equal(a.StartsAt)
}

func WithCooldown(window Window, cooldown time.Duration) Window {
	return Window{StartsAt: window.StartsAt.Add(-cooldown), EndsAt: window.EndsAt.Add(cooldown)}
}

func (request PlanRequest) Validate() error {
	if request.ID == "" || request.TenantID == "" || request.BarnID == "" {
		return fmt.Errorf("%w: plan request identity", domain.ErrInvalid)
	}
	if len(request.OperatorIDs) == 0 || len(request.FeederIDs) == 0 {
		return fmt.Errorf("%w: plan resources", domain.ErrInvalid)
	}
	if request.Duration <= 0 || request.Duration > 6*time.Hour {
		return fmt.Errorf("%w: plan duration", domain.ErrInvalid)
	}
	if !request.LatestEnd.After(request.EarliestStart) || request.EarliestStart.Add(request.Duration).After(request.LatestEnd) {
		return fmt.Errorf("%w: plan horizon", domain.ErrInvalid)
	}
	return nil
}

func (assignment Assignment) Validate() error {
	if assignment.ID == "" || assignment.TenantID == "" || assignment.PlanID == "" || assignment.ResourceID == "" {
		return fmt.Errorf("%w: assignment identity", domain.ErrInvalid)
	}
	if err := assignment.Window.Validate(); err != nil {
		return err
	}
	switch assignment.ResourceKind {
	case ResourceOperator, ResourceFeeder, ResourceBarn:
		return nil
	default:
		return fmt.Errorf("%w: resource kind", domain.ErrInvalid)
	}
}

func Allocate(requests []PlanRequest, existing []Assignment, constraints []Constraint, slot time.Duration) ([]Allocation, error) {
	if slot <= 0 {
		return nil, fmt.Errorf("%w: schedule slot", domain.ErrInvalid)
	}
	ordered := append([]PlanRequest(nil), requests...)
	for _, request := range ordered {
		if err := request.Validate(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			if ordered[i].LatestEnd.Equal(ordered[j].LatestEnd) {
				return ordered[i].ID < ordered[j].ID
			}
			return ordered[i].LatestEnd.Before(ordered[j].LatestEnd)
		}
		return ordered[i].Priority > ordered[j].Priority
	})
	booked := append([]Assignment(nil), existing...)
	results := make([]Allocation, 0, len(ordered))
	for _, request := range ordered {
		allocation := Allocation{Request: request}
		for start := request.EarliestStart; !start.Add(request.Duration).After(request.LatestEnd); start = start.Add(slot) {
			window := Window{StartsAt: start, EndsAt: start.Add(request.Duration)}
			operator := firstAvailable(ResourceOperator, request.OperatorIDs, window, booked, constraints)
			if operator == "" {
				continue
			}
			feeder := firstAvailable(ResourceFeeder, request.FeederIDs, window, booked, constraints)
			if feeder == "" {
				continue
			}
			if !resourceAvailable(ResourceBarn, request.BarnID, window, booked, constraints) {
				continue
			}
			allocation.Scheduled = true
			allocation.Assignments = []Assignment{
				newAssignment(request, ResourceOperator, operator, window),
				newAssignment(request, ResourceFeeder, feeder, window),
				newAssignment(request, ResourceBarn, request.BarnID, window),
			}
			booked = append(booked, allocation.Assignments...)
			break
		}
		if !allocation.Scheduled {
			allocation.Reason = "no operator, feeder and barn window satisfies all constraints"
		}
		results = append(results, allocation)
	}
	return results, nil
}

func firstAvailable(kind ResourceKind, ids []string, window Window, booked []Assignment, constraints []Constraint) string {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for _, id := range ordered {
		if resourceAvailable(kind, id, window, booked, constraints) {
			return id
		}
	}
	return ""
}

func resourceAvailable(kind ResourceKind, id string, window Window, booked []Assignment, constraints []Constraint) bool {
	constraint := findConstraint(kind, id, constraints)
	checkWindow := WithCooldown(window, constraint.Cooldown)
	for _, unavailable := range constraint.Unavailable {
		if Overlaps(checkWindow, unavailable) {
			return false
		}
	}
	dayUsage := window.Duration()
	for _, assignment := range booked {
		if assignment.Status == "cancelled" || assignment.ResourceKind != kind || assignment.ResourceID != id {
			continue
		}
		if Overlaps(checkWindow, WithCooldown(assignment.Window, constraint.Cooldown)) {
			return false
		}
		if sameDay(assignment.Window.StartsAt, window.StartsAt) {
			dayUsage += assignment.Window.Duration()
		}
	}
	return constraint.DailyLimit <= 0 || dayUsage <= constraint.DailyLimit
}

func findConstraint(kind ResourceKind, id string, constraints []Constraint) Constraint {
	for _, constraint := range constraints {
		if constraint.ResourceKind == kind && constraint.ResourceID == id {
			return constraint
		}
	}
	return Constraint{ResourceKind: kind, ResourceID: id}
}

func newAssignment(request PlanRequest, kind ResourceKind, resource string, window Window) Assignment {
	return Assignment{ID: fmt.Sprintf("assignment-%s-%s-%s", request.ID, kind, resource), TenantID: request.TenantID, PlanID: request.ID, ResourceKind: kind, ResourceID: resource, Window: window, Priority: request.Priority, Status: "reserved", Version: 1}
}

func Cancel(assignments []Assignment, planID string, expectedVersions map[string]int64) ([]Assignment, error) {
	updated := append([]Assignment(nil), assignments...)
	found := 0
	for i := range updated {
		if updated[i].PlanID != planID || updated[i].Status == "cancelled" {
			continue
		}
		expected, ok := expectedVersions[updated[i].ID]
		if !ok || expected != updated[i].Version {
			return nil, fmt.Errorf("%w: assignment version", domain.ErrConflict)
		}
		updated[i].Status = "cancelled"
		updated[i].Version++
		found++
	}
	if found == 0 {
		return nil, fmt.Errorf("%w: plan assignments", domain.ErrNotFound)
	}
	return updated, nil
}

func Move(assignments []Assignment, planID string, window Window, expectedVersions map[string]int64, constraints []Constraint) ([]Assignment, error) {
	if err := window.Validate(); err != nil {
		return nil, err
	}
	others := make([]Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.PlanID != planID {
			others = append(others, assignment)
		}
	}
	updated := append([]Assignment(nil), assignments...)
	found := 0
	for i := range updated {
		if updated[i].PlanID != planID || updated[i].Status == "cancelled" {
			continue
		}
		expected, ok := expectedVersions[updated[i].ID]
		if !ok || expected != updated[i].Version {
			return nil, fmt.Errorf("%w: assignment version", domain.ErrConflict)
		}
		if !resourceAvailable(updated[i].ResourceKind, updated[i].ResourceID, window, others, constraints) {
			return nil, fmt.Errorf("%w: moved window conflicts for %s", domain.ErrConflict, updated[i].ResourceID)
		}
		updated[i].Window = window
		updated[i].Version++
		found++
	}
	if found == 0 {
		return nil, fmt.Errorf("%w: plan assignments", domain.ErrNotFound)
	}
	return updated, nil
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.In(a.Location()).Date()
	return ay == by && am == bm && ad == bd
}
