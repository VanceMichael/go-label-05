package equipment

import (
	"testing"
	"time"
)

func TestMachineCapacityAndService(t *testing.T) {
	m := Machine{Status: StatusAvailable, CapacityKg: 100, LastServiceAt: time.Now().Add(-time.Hour), ServiceInterval: 2 * time.Hour}
	if err := m.CanHandle(50, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := m.CanHandle(101, time.Now()); err == nil {
		t.Fatal("over capacity accepted")
	}
}
func TestReservationOverlap(t *testing.T) {
	n := time.Now()
	a := Reservation{StartsAt: n, EndsAt: n.Add(time.Hour)}
	b := Reservation{StartsAt: n.Add(30 * time.Minute), EndsAt: n.Add(2 * time.Hour)}
	if !Overlaps(a, b) {
		t.Fatal("overlap missed")
	}
}
