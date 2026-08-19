package transport

import (
	"errors"
	"testing"
	"time"

	"go-base/internal/domain"
)

func resources(now time.Time) (Shipment, Vehicle, Driver) {
	shipment := Shipment{ID: "shipment-1", TenantID: "tenant-1", BatchIDs: []string{"batch-1"}, VehicleID: "vehicle-1", DriverID: "driver-1", DestinationID: "plant-1", ScheduledWindow: Window{StartsAt: now.Add(time.Hour), EndsAt: now.Add(3 * time.Hour)}, WeightKg: 8000, Status: "draft", Version: 1}
	vehicle := Vehicle{ID: "vehicle-1", TenantID: "tenant-1", Plate: "甘A10001", CapacityKg: 10000, Status: VehicleAvailable, LastCleanedAt: now.Add(-time.Hour), LastServicedAt: now.AddDate(0, -1, 0), Version: 1}
	driver := Driver{ID: "driver-1", TenantID: "tenant-1", Name: "Driver", LicenseExpires: now.AddDate(1, 0, 0), Qualified: true}
	return shipment, vehicle, driver
}

func TestAssignChecksCapacityAndCopiesState(t *testing.T) {
	now := time.Now().UTC()
	shipment, vehicle, driver := resources(now)
	assigned, assignedVehicle, err := Assign(shipment, vehicle, driver, now)
	if err != nil || assigned.Status != "assigned" || assignedVehicle.Status != VehicleAssigned {
		t.Fatalf("shipment=%+v vehicle=%+v error=%v", assigned, assignedVehicle, err)
	}
	if shipment.Status != "draft" || vehicle.Status != VehicleAvailable {
		t.Fatal("assign mutated input")
	}
	shipment.WeightKg = 11000
	if _, _, err := Assign(shipment, vehicle, driver, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestDepartureAndArrivalLifecycle(t *testing.T) {
	now := time.Now().UTC()
	shipment, vehicle, driver := resources(now)
	shipment, vehicle, _ = Assign(shipment, vehicle, driver, now)
	departure := now.Add(time.Hour)
	shipment, vehicle, err := Depart(shipment, vehicle, "seal-1", departure)
	if err != nil || shipment.Status != "in_transit" || vehicle.Status != VehicleInTransit {
		t.Fatalf("error=%v", err)
	}
	ticket := WeighbridgeTicket{ID: "ticket-1", TenantID: shipment.TenantID, ShipmentID: shipment.ID, GrossKg: 13000, TareKg: 5000, NetKg: 8000, MeasuredAt: departure.Add(time.Hour), OperatorID: "scale-1"}
	shipment, vehicle, err = Arrive(shipment, vehicle, ticket, .01, departure.Add(time.Hour))
	if err != nil || shipment.Status != "arrived" || vehicle.Status != VehicleCleaning {
		t.Fatalf("shipment=%+v vehicle=%+v error=%v", shipment, vehicle, err)
	}
}

func TestArrivalRejectsWeightDifference(t *testing.T) {
	now := time.Now().UTC()
	shipment, vehicle, driver := resources(now)
	shipment, vehicle, _ = Assign(shipment, vehicle, driver, now)
	shipment, vehicle, _ = Depart(shipment, vehicle, "seal", now.Add(time.Hour))
	ticket := WeighbridgeTicket{ID: "ticket", TenantID: shipment.TenantID, ShipmentID: shipment.ID, GrossKg: 12500, TareKg: 5000, NetKg: 7500, MeasuredAt: now.Add(2 * time.Hour), OperatorID: "scale"}
	if _, _, err := Arrive(shipment, vehicle, ticket, .02, now.Add(2*time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
}

func TestBuildRouteWaitsForWindowsAndTracksLoad(t *testing.T) {
	now := time.Now().UTC()
	shipment, _, _ := resources(now)
	stops := []RouteStop{
		{Sequence: 1, LocationID: "farm", ServiceWindow: Window{StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(4 * time.Hour)}, ServiceTime: 15 * time.Minute, WeightDeltaKg: 100},
		{Sequence: 2, LocationID: "plant", ServiceWindow: Window{StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(6 * time.Hour)}, ServiceTime: 20 * time.Minute, WeightDeltaKg: -8100},
	}
	route, err := BuildRoute(shipment, stops, map[string]time.Duration{"origin->farm": 20 * time.Minute, "farm->plant": 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if route.PeakLoadKg != 8100 || !route.EndsAt.Equal(now.Add(3*time.Hour+5*time.Minute)) {
		t.Fatalf("route=%+v", route)
	}
}

func TestDetectConflictsUsesTenantVehicleAndDriver(t *testing.T) {
	now := time.Now().UTC()
	one, _, _ := resources(now)
	two := one
	two.ID = "shipment-2"
	two.BatchIDs = []string{"batch-2"}
	three := two
	three.ID = "shipment-3"
	three.TenantID = "tenant-2"
	conflicts := DetectConflicts([]Shipment{one, two, three})
	if len(conflicts[one.ID]) != 1 || conflicts[one.ID][0] != two.ID || len(conflicts[three.ID]) != 0 {
		t.Fatalf("conflicts=%+v", conflicts)
	}
}
