package schedule

import (
	"errors"
	"testing"
	"time"

	"go-base/internal/domain"
)

func request(id string, start time.Time, priority int) PlanRequest {
	return PlanRequest{ID: id, TenantID: "tenant-1", BarnID: "barn-1", OperatorIDs: []string{"operator-2", "operator-1"}, FeederIDs: []string{"feeder-1"}, EarliestStart: start, LatestEnd: start.Add(4 * time.Hour), Duration: time.Hour, Priority: priority}
}

func TestWindowOverlapExcludesTouchingEndpoints(t *testing.T) {
	now := time.Now().UTC()
	a := Window{StartsAt: now, EndsAt: now.Add(time.Hour)}
	b := Window{StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)}
	if Overlaps(a, b) || !Touches(a, b) {
		t.Fatalf("overlap=%v touches=%v", Overlaps(a, b), Touches(a, b))
	}
}

func TestAllocateHonorsPriorityAndStableResourceChoice(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	results, err := Allocate([]PlanRequest{request("low", now, 1), request("high", now, 5)}, nil, nil, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Request.ID != "high" || !results[0].Scheduled || !results[1].Scheduled {
		t.Fatalf("results=%+v", results)
	}
	if results[0].Assignments[0].ResourceID != "operator-1" {
		t.Fatalf("assignment=%+v", results[0].Assignments)
	}
	if !results[1].Assignments[0].Window.StartsAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("second window=%+v", results[1].Assignments[0].Window)
	}
}

func TestAllocateHonorsUnavailableAndDailyLimit(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	r := request("plan", now, 1)
	r.OperatorIDs = []string{"operator-1"}
	constraints := []Constraint{
		{ResourceKind: ResourceOperator, ResourceID: "operator-1", Unavailable: []Window{{StartsAt: now, EndsAt: now.Add(2 * time.Hour)}}, DailyLimit: 2 * time.Hour},
	}
	results, err := Allocate([]PlanRequest{r}, nil, constraints, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Scheduled || !results[0].Assignments[0].Window.StartsAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("results=%+v", results)
	}
}

func TestAllocateReportsUnschedulableRequest(t *testing.T) {
	now := time.Now().UTC()
	r := request("plan", now, 1)
	constraints := []Constraint{{ResourceKind: ResourceBarn, ResourceID: "barn-1", Unavailable: []Window{{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(8 * time.Hour)}}}}
	results, err := Allocate([]PlanRequest{r}, nil, constraints, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Scheduled || results[0].Reason == "" {
		t.Fatalf("results=%+v", results)
	}
}

func TestCancelRequiresEveryExpectedVersion(t *testing.T) {
	now := time.Now().UTC()
	assignments := []Assignment{
		{ID: "a", PlanID: "p", Status: "reserved", Version: 2, Window: Window{StartsAt: now, EndsAt: now.Add(time.Hour)}},
		{ID: "b", PlanID: "p", Status: "reserved", Version: 3, Window: Window{StartsAt: now, EndsAt: now.Add(time.Hour)}},
	}
	if _, err := Cancel(assignments, "p", map[string]int64{"a": 2}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	updated, err := Cancel(assignments, "p", map[string]int64{"a": 2, "b": 3})
	if err != nil || updated[0].Status != "cancelled" || updated[1].Status != "cancelled" {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}
	if assignments[0].Status != "reserved" {
		t.Fatal("cancel mutated caller assignments")
	}
}

func TestMoveRejectsConflictWithoutPartialMutation(t *testing.T) {
	now := time.Now().UTC()
	assignments := []Assignment{
		{ID: "moving", PlanID: "p1", ResourceKind: ResourceFeeder, ResourceID: "f1", Status: "reserved", Version: 1, Window: Window{StartsAt: now, EndsAt: now.Add(time.Hour)}},
		{ID: "existing", PlanID: "p2", ResourceKind: ResourceFeeder, ResourceID: "f1", Status: "reserved", Version: 1, Window: Window{StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(3 * time.Hour)}},
	}
	_, err := Move(assignments, "p1", Window{StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(3 * time.Hour)}, map[string]int64{"moving": 1}, nil)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	if !assignments[0].Window.StartsAt.Equal(now) {
		t.Fatal("failed move mutated input")
	}
}
