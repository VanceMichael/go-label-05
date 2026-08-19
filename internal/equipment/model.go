package equipment

import (
	"fmt"
	"go-base/internal/domain"
	"time"
)

type Status string

const (
	StatusAvailable   Status = "available"
	StatusReserved    Status = "reserved"
	StatusRunning     Status = "running"
	StatusMaintenance Status = "maintenance"
	StatusRetired     Status = "retired"
)

type Machine struct {
	ID, TenantID, BarnID, Name string
	Status                     Status
	CapacityKg                 float64
	Version                    int64
	LastServiceAt              time.Time
	ServiceInterval            time.Duration
}
type Reservation struct {
	ID, TenantID, MachineID, PlanID string
	StartsAt, EndsAt                time.Time
	Status                          string
	Version                         int64
}

func (m Machine) CanHandle(feedKg float64, at time.Time) error {
	if m.Status != StatusAvailable && m.Status != StatusReserved {
		return fmt.Errorf("%w: machine status %s", domain.ErrConflict, m.Status)
	}
	if feedKg <= 0 || feedKg > m.CapacityKg {
		return fmt.Errorf("%w: machine capacity", domain.ErrInvalid)
	}
	if !m.LastServiceAt.IsZero() && at.Sub(m.LastServiceAt) > m.ServiceInterval {
		return fmt.Errorf("%w: machine service overdue", domain.ErrConflict)
	}
	return nil
}
func (r Reservation) Validate() error {
	if r.ID == "" || r.MachineID == "" || r.PlanID == "" {
		return fmt.Errorf("%w: reservation identity", domain.ErrInvalid)
	}
	if !r.EndsAt.After(r.StartsAt) {
		return fmt.Errorf("%w: reservation window", domain.ErrInvalid)
	}
	if r.EndsAt.Sub(r.StartsAt) > 8*time.Hour {
		return fmt.Errorf("%w: reservation too long", domain.ErrInvalid)
	}
	return nil
}
func Overlaps(a, b Reservation) bool {
	return a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt)
}
func Transition(current, next Status) error {
	allowed := map[Status]map[Status]bool{StatusAvailable: {StatusReserved: true, StatusMaintenance: true, StatusRetired: true}, StatusReserved: {StatusRunning: true, StatusAvailable: true}, StatusRunning: {StatusAvailable: true, StatusMaintenance: true}, StatusMaintenance: {StatusAvailable: true, StatusRetired: true}, StatusRetired: {}}
	if !allowed[current][next] {
		return fmt.Errorf("%w: equipment %s to %s", domain.ErrConflict, current, next)
	}
	return nil
}
