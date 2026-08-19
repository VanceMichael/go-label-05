package transport

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"go-base/internal/domain"
)

type VehicleStatus string

const (
	VehicleAvailable   VehicleStatus = "available"
	VehicleAssigned    VehicleStatus = "assigned"
	VehicleInTransit   VehicleStatus = "in_transit"
	VehicleCleaning    VehicleStatus = "cleaning"
	VehicleMaintenance VehicleStatus = "maintenance"
)

type Vehicle struct {
	ID             string
	TenantID       string
	Plate          string
	CapacityKg     float64
	Status         VehicleStatus
	LastCleanedAt  time.Time
	LastServicedAt time.Time
	Version        int64
}

type Driver struct {
	ID             string
	TenantID       string
	Name           string
	LicenseExpires time.Time
	Qualified      bool
	Disabled       bool
}

type Shipment struct {
	ID              string
	TenantID        string
	BatchIDs        []string
	VehicleID       string
	DriverID        string
	DestinationID   string
	ScheduledWindow Window
	WeightKg        float64
	Status          string
	DepartedAt      *time.Time
	ArrivedAt       *time.Time
	SealNumber      string
	Version         int64
}

type Window struct {
	StartsAt time.Time
	EndsAt   time.Time
}

type WeighbridgeTicket struct {
	ID         string
	TenantID   string
	ShipmentID string
	GrossKg    float64
	TareKg     float64
	NetKg      float64
	MeasuredAt time.Time
	OperatorID string
}

type RouteStop struct {
	Sequence      int
	LocationID    string
	ServiceWindow Window
	ServiceTime   time.Duration
	WeightDeltaKg float64
}

type Route struct {
	ShipmentID string
	Stops      []RouteStop
	StartsAt   time.Time
	EndsAt     time.Time
	PeakLoadKg float64
}

func (v Vehicle) Validate(now time.Time) error {
	if v.ID == "" || v.TenantID == "" || strings.TrimSpace(v.Plate) == "" {
		return fmt.Errorf("%w: vehicle identity", domain.ErrInvalid)
	}
	if v.CapacityKg <= 0 {
		return fmt.Errorf("%w: vehicle capacity", domain.ErrInvalid)
	}
	if now.Sub(v.LastServicedAt) > 180*24*time.Hour {
		return fmt.Errorf("%w: vehicle service overdue", domain.ErrConflict)
	}
	if now.Sub(v.LastCleanedAt) > 48*time.Hour && v.Status == VehicleAvailable {
		return fmt.Errorf("%w: vehicle cleaning overdue", domain.ErrConflict)
	}
	return nil
}

func (d Driver) Validate(at time.Time) error {
	if d.ID == "" || d.TenantID == "" || d.Name == "" {
		return fmt.Errorf("%w: driver identity", domain.ErrInvalid)
	}
	if d.Disabled || !d.Qualified {
		return fmt.Errorf("%w: driver is not qualified", domain.ErrForbidden)
	}
	if !d.LicenseExpires.After(at) {
		return fmt.Errorf("%w: driver license expired", domain.ErrConflict)
	}
	return nil
}

func (s Shipment) Validate() error {
	if s.ID == "" || s.TenantID == "" || s.VehicleID == "" || s.DriverID == "" || s.DestinationID == "" {
		return fmt.Errorf("%w: shipment identity", domain.ErrInvalid)
	}
	if len(s.BatchIDs) == 0 || s.WeightKg <= 0 {
		return fmt.Errorf("%w: shipment contents", domain.ErrInvalid)
	}
	if !s.ScheduledWindow.EndsAt.After(s.ScheduledWindow.StartsAt) {
		return fmt.Errorf("%w: shipment window", domain.ErrInvalid)
	}
	seen := make(map[string]struct{}, len(s.BatchIDs))
	for _, batchID := range s.BatchIDs {
		if batchID == "" {
			return fmt.Errorf("%w: empty batch ID", domain.ErrInvalid)
		}
		if _, exists := seen[batchID]; exists {
			return fmt.Errorf("%w: duplicate batch %s", domain.ErrConflict, batchID)
		}
		seen[batchID] = struct{}{}
	}
	return nil
}

func Assign(shipment Shipment, vehicle Vehicle, driver Driver, at time.Time) (Shipment, Vehicle, error) {
	if err := shipment.Validate(); err != nil {
		return shipment, vehicle, err
	}
	if err := vehicle.Validate(at); err != nil {
		return shipment, vehicle, err
	}
	if err := driver.Validate(at); err != nil {
		return shipment, vehicle, err
	}
	if shipment.TenantID != vehicle.TenantID || shipment.TenantID != driver.TenantID {
		return shipment, vehicle, fmt.Errorf("%w: shipment tenant scope", domain.ErrConflict)
	}
	if shipment.VehicleID != vehicle.ID || shipment.DriverID != driver.ID {
		return shipment, vehicle, fmt.Errorf("%w: shipment resource binding", domain.ErrConflict)
	}
	if vehicle.Status != VehicleAvailable {
		return shipment, vehicle, fmt.Errorf("%w: vehicle status %s", domain.ErrConflict, vehicle.Status)
	}
	if shipment.WeightKg > vehicle.CapacityKg {
		return shipment, vehicle, fmt.Errorf("%w: shipment exceeds vehicle capacity", domain.ErrConflict)
	}
	outShipment := shipment
	outShipment.Status = "assigned"
	outShipment.Version++
	outVehicle := vehicle
	outVehicle.Status = VehicleAssigned
	outVehicle.Version++
	return outShipment, outVehicle, nil
}

func Depart(shipment Shipment, vehicle Vehicle, seal string, at time.Time) (Shipment, Vehicle, error) {
	if shipment.Status != "assigned" || vehicle.Status != VehicleAssigned {
		return shipment, vehicle, fmt.Errorf("%w: shipment is not assigned", domain.ErrConflict)
	}
	if shipment.VehicleID != vehicle.ID || shipment.TenantID != vehicle.TenantID {
		return shipment, vehicle, fmt.Errorf("%w: shipment vehicle mismatch", domain.ErrConflict)
	}
	if strings.TrimSpace(seal) == "" {
		return shipment, vehicle, fmt.Errorf("%w: seal number", domain.ErrInvalid)
	}
	if at.Before(shipment.ScheduledWindow.StartsAt.Add(-30*time.Minute)) || at.After(shipment.ScheduledWindow.EndsAt) {
		return shipment, vehicle, fmt.Errorf("%w: departure outside window", domain.ErrConflict)
	}
	outShipment := shipment
	outShipment.Status = "in_transit"
	outShipment.SealNumber = strings.TrimSpace(seal)
	outShipment.DepartedAt = &at
	outShipment.Version++
	outVehicle := vehicle
	outVehicle.Status = VehicleInTransit
	outVehicle.Version++
	return outShipment, outVehicle, nil
}

func Arrive(shipment Shipment, vehicle Vehicle, ticket WeighbridgeTicket, tolerance float64, at time.Time) (Shipment, Vehicle, error) {
	if shipment.Status != "in_transit" || vehicle.Status != VehicleInTransit {
		return shipment, vehicle, fmt.Errorf("%w: shipment is not in transit", domain.ErrConflict)
	}
	if shipment.DepartedAt == nil || at.Before(*shipment.DepartedAt) {
		return shipment, vehicle, fmt.Errorf("%w: arrival time", domain.ErrInvalid)
	}
	if ticket.ShipmentID != shipment.ID || ticket.TenantID != shipment.TenantID {
		return shipment, vehicle, fmt.Errorf("%w: weighbridge ticket scope", domain.ErrConflict)
	}
	if err := ticket.Validate(); err != nil {
		return shipment, vehicle, err
	}
	if tolerance < 0 || tolerance > .2 {
		return shipment, vehicle, fmt.Errorf("%w: shipment tolerance", domain.ErrInvalid)
	}
	difference := abs(ticket.NetKg-shipment.WeightKg) / shipment.WeightKg
	if difference > tolerance {
		return shipment, vehicle, fmt.Errorf("%w: weighbridge difference %.2f%%", domain.ErrConflict, difference*100)
	}
	outShipment := shipment
	outShipment.Status = "arrived"
	outShipment.ArrivedAt = &at
	outShipment.Version++
	outVehicle := vehicle
	outVehicle.Status = VehicleCleaning
	outVehicle.Version++
	return outShipment, outVehicle, nil
}

func (ticket WeighbridgeTicket) Validate() error {
	if ticket.ID == "" || ticket.TenantID == "" || ticket.ShipmentID == "" || ticket.OperatorID == "" {
		return fmt.Errorf("%w: weighbridge ticket identity", domain.ErrInvalid)
	}
	if ticket.GrossKg <= 0 || ticket.TareKg <= 0 || ticket.GrossKg <= ticket.TareKg {
		return fmt.Errorf("%w: weighbridge values", domain.ErrInvalid)
	}
	calculated := ticket.GrossKg - ticket.TareKg
	if abs(calculated-ticket.NetKg) > .01 {
		return fmt.Errorf("%w: weighbridge net mismatch", domain.ErrConflict)
	}
	return nil
}

func BuildRoute(shipment Shipment, stops []RouteStop, travelTimes map[string]time.Duration) (Route, error) {
	if err := shipment.Validate(); err != nil {
		return Route{}, err
	}
	if len(stops) == 0 {
		return Route{}, fmt.Errorf("%w: route stops", domain.ErrInvalid)
	}
	ordered := append([]RouteStop(nil), stops...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	current := shipment.ScheduledWindow.StartsAt
	load := shipment.WeightKg
	peak := load
	previous := "origin"
	for i := range ordered {
		if ordered[i].Sequence != i+1 || ordered[i].LocationID == "" {
			return Route{}, fmt.Errorf("%w: route stop sequence", domain.ErrInvalid)
		}
		travel, exists := travelTimes[previous+"->"+ordered[i].LocationID]
		if !exists || travel < 0 {
			return Route{}, fmt.Errorf("%w: travel time %s to %s", domain.ErrNotFound, previous, ordered[i].LocationID)
		}
		current = current.Add(travel)
		if current.Before(ordered[i].ServiceWindow.StartsAt) {
			current = ordered[i].ServiceWindow.StartsAt
		}
		if current.After(ordered[i].ServiceWindow.EndsAt) {
			return Route{}, fmt.Errorf("%w: missed service window at %s", domain.ErrConflict, ordered[i].LocationID)
		}
		current = current.Add(ordered[i].ServiceTime)
		load += ordered[i].WeightDeltaKg
		if load < -0.0001 || load > shipment.WeightKg*1.2 {
			return Route{}, fmt.Errorf("%w: invalid route load", domain.ErrConflict)
		}
		if load > peak {
			peak = load
		}
		previous = ordered[i].LocationID
	}
	return Route{ShipmentID: shipment.ID, Stops: ordered, StartsAt: shipment.ScheduledWindow.StartsAt, EndsAt: current, PeakLoadKg: peak}, nil
}

func DetectConflicts(shipments []Shipment) map[string][]string {
	result := make(map[string][]string)
	for i := range shipments {
		if shipments[i].Status == "cancelled" || shipments[i].Status == "arrived" {
			continue
		}
		for j := i + 1; j < len(shipments); j++ {
			if shipments[j].Status == "cancelled" || shipments[j].Status == "arrived" || shipments[i].TenantID != shipments[j].TenantID {
				continue
			}
			if !overlaps(shipments[i].ScheduledWindow, shipments[j].ScheduledWindow) {
				continue
			}
			if shipments[i].VehicleID == shipments[j].VehicleID || shipments[i].DriverID == shipments[j].DriverID {
				result[shipments[i].ID] = append(result[shipments[i].ID], shipments[j].ID)
				result[shipments[j].ID] = append(result[shipments[j].ID], shipments[i].ID)
			}
		}
	}
	for key := range result {
		sort.Strings(result[key])
	}
	return result
}

func overlaps(a, b Window) bool {
	return a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt)
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
