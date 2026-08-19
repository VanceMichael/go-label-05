package maintenance

import (
	"errors"
	"go-base/internal/domain"
	"testing"
	"time"
)

func asset(now time.Time) Asset {
	return Asset{ID: "asset-1", TenantID: "tenant-1", BarnID: "barn-1", Name: "Mixer", Category: "feeder", Status: AssetOperational, MeterHours: 1000, ServiceEvery: 100, LastServiceHour: 950, Version: 1}
}
func TestUsageMarksServiceDue(t *testing.T) {
	now := time.Now()
	a := asset(now)
	a.ServiceEvery = 74
	out, err := a.RecordUsage(24)
	if err != nil || out.Status != AssetDegraded || out.MeterHours != 1024 || out.Version != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if a.MeterHours != 1000 {
		t.Fatal("input mutated")
	}
}
func TestOpenAssignStartCompleteWorkOrder(t *testing.T) {
	now := time.Now()
	a := asset(now)
	window := Window{StartsAt: now.Add(time.Hour), EndsAt: now.Add(3 * time.Hour)}
	order, a, err := Open("wo-1", a, "manager", "scheduled service", 3, window, now)
	if err != nil {
		t.Fatal(err)
	}
	order, err = Assign(order, "tech-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	order, a, err = Start(order, a, "tech-1", now.Add(time.Hour), 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	report := ServiceReport{WorkOrderID: order.ID, AssetID: a.ID, Technician: "tech-1", LaborMins: 60, Resolution: "replaced bearing", MeterHours: 1050, CompletedAt: now.Add(2 * time.Hour), Parts: []Part{{Code: "bearing", Name: "Bearing", Quantity: 1, UnitCost: 1000, Serialized: true, Serials: []string{"S1"}}}}
	order, a, err = Complete(order, a, report, 3, 3)
	if err != nil || order.Status != "completed" || a.Status != AssetOperational || a.LastServiceHour != 1050 {
		t.Fatalf("order=%+v asset=%+v err=%v", order, a, err)
	}
}
func TestStartRejectsWrongTechnician(t *testing.T) {
	now := time.Now()
	a := asset(now)
	order, a, _ := Open("wo", a, "manager", "repair", 1, Window{StartsAt: now, EndsAt: now.Add(time.Hour)}, now)
	order, _ = Assign(order, "tech-1", 1)
	if _, _, err := Start(order, a, "tech-2", now, 2, 2); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestCompleteRejectsDuplicateSerial(t *testing.T) {
	now := time.Now()
	a := asset(now)
	order := WorkOrder{ID: "wo", TenantID: a.TenantID, AssetID: a.ID, AssignedTo: "tech", Status: "in_progress", MeterAtOpen: a.MeterHours, Version: 1, StartedAt: &now}
	a.Status = AssetServicing
	report := ServiceReport{WorkOrderID: "wo", AssetID: a.ID, Technician: "tech", LaborMins: 1, Resolution: "done", MeterHours: a.MeterHours, CompletedAt: now.Add(time.Minute), Parts: []Part{{Code: "a", Name: "A", Quantity: 2, Serialized: true, Serials: []string{"x", "x"}}}}
	if _, _, err := Complete(order, a, report, 1, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
}
func TestBacklogRanksOverdueThenPriority(t *testing.T) {
	now := time.Now()
	orders := []WorkOrder{{ID: "future", Status: "open", Priority: 5, ScheduledWindow: Window{StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)}}, {ID: "old-low", Status: "open", Priority: 1, ScheduledWindow: Window{StartsAt: now.Add(-3 * time.Hour), EndsAt: now.Add(-2 * time.Hour)}}, {ID: "old-high", Status: "open", Priority: 4, ScheduledWindow: Window{StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}}}
	ranked := RankBacklog(orders, now)
	if ranked[0].ID != "old-high" || ranked[1].ID != "old-low" || ranked[2].ID != "future" {
		t.Fatal(ranked)
	}
}
func TestDowntimeMergesOverlapAndClipsWindow(t *testing.T) {
	now := time.Now()
	window := Window{StartsAt: now, EndsAt: now.Add(4 * time.Hour)}
	events := []Downtime{{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}, {StartsAt: now.Add(30 * time.Minute), EndsAt: now.Add(2 * time.Hour)}, {StartsAt: now.Add(3 * time.Hour), EndsAt: now.Add(5 * time.Hour)}}
	total, err := CalculateDowntime(events, window)
	if err != nil || total != 3*time.Hour {
		t.Fatalf("total=%v err=%v", total, err)
	}
}
