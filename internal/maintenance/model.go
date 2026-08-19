package maintenance

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"go-base/internal/domain"
)

type AssetStatus string

const (
	AssetOperational AssetStatus = "operational"
	AssetDegraded    AssetStatus = "degraded"
	AssetStopped     AssetStatus = "stopped"
	AssetServicing   AssetStatus = "servicing"
	AssetRetired     AssetStatus = "retired"
)

type Asset struct {
	ID              string
	TenantID        string
	BarnID          string
	Name            string
	Category        string
	Status          AssetStatus
	MeterHours      float64
	ServiceEvery    float64
	LastServiceHour float64
	Version         int64
}

type WorkOrder struct {
	ID              string
	TenantID        string
	AssetID         string
	RequestedBy     string
	AssignedTo      string
	Priority        int
	Summary         string
	Status          string
	MeterAtOpen     float64
	ScheduledWindow Window
	OpenedAt        time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	Version         int64
}

type Window struct {
	StartsAt time.Time
	EndsAt   time.Time
}

type Part struct {
	Code       string
	Name       string
	Quantity   int
	UnitCost   int64
	Serialized bool
	Serials    []string
}

type ServiceReport struct {
	WorkOrderID string
	AssetID     string
	Technician  string
	Parts       []Part
	LaborMins   int
	Resolution  string
	MeterHours  float64
	CompletedAt time.Time
}

type Downtime struct {
	AssetID  string
	StartsAt time.Time
	EndsAt   time.Time
	Reason   string
}

func (a Asset) Validate() error {
	if a.ID == "" || a.TenantID == "" || a.BarnID == "" || strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("%w: asset identity", domain.ErrInvalid)
	}
	if a.MeterHours < 0 || a.ServiceEvery <= 0 || a.LastServiceHour < 0 || a.LastServiceHour > a.MeterHours {
		return fmt.Errorf("%w: asset meter", domain.ErrInvalid)
	}
	switch a.Status {
	case AssetOperational, AssetDegraded, AssetStopped, AssetServicing, AssetRetired:
		return nil
	default:
		return fmt.Errorf("%w: asset status", domain.ErrInvalid)
	}
}

func (a Asset) ServiceDue() (bool, float64) {
	remaining := a.ServiceEvery - (a.MeterHours - a.LastServiceHour)
	return remaining <= 0, remaining
}

func (a Asset) RecordUsage(hours float64) (Asset, error) {
	if err := a.Validate(); err != nil {
		return a, err
	}
	if hours <= 0 || hours > 24 {
		return a, fmt.Errorf("%w: usage hours", domain.ErrInvalid)
	}
	if a.Status != AssetOperational && a.Status != AssetDegraded {
		return a, fmt.Errorf("%w: asset cannot record usage in %s", domain.ErrConflict, a.Status)
	}
	out := a
	out.MeterHours += hours
	out.Version++
	if due, _ := out.ServiceDue(); due && out.Status == AssetOperational {
		out.Status = AssetDegraded
	}
	return out, nil
}

func Open(id string, asset Asset, actor, summary string, priority int, window Window, at time.Time) (WorkOrder, Asset, error) {
	if err := asset.Validate(); err != nil {
		return WorkOrder{}, asset, err
	}
	if id == "" || actor == "" || strings.TrimSpace(summary) == "" || priority < 1 || priority > 5 {
		return WorkOrder{}, asset, fmt.Errorf("%w: work order request", domain.ErrInvalid)
	}
	if !window.EndsAt.After(window.StartsAt) || window.EndsAt.Sub(window.StartsAt) > 72*time.Hour {
		return WorkOrder{}, asset, fmt.Errorf("%w: work order window", domain.ErrInvalid)
	}
	if asset.Status == AssetRetired || asset.Status == AssetServicing {
		return WorkOrder{}, asset, fmt.Errorf("%w: asset status %s", domain.ErrConflict, asset.Status)
	}
	order := WorkOrder{ID: id, TenantID: asset.TenantID, AssetID: asset.ID, RequestedBy: actor, Priority: priority, Summary: strings.TrimSpace(summary), Status: "open", MeterAtOpen: asset.MeterHours, ScheduledWindow: window, OpenedAt: at, Version: 1}
	out := asset
	if out.Status == AssetOperational {
		out.Status = AssetDegraded
	}
	out.Version++
	return order, out, nil
}

func Assign(order WorkOrder, technician string, expectedVersion int64) (WorkOrder, error) {
	if order.Status != "open" || order.Version != expectedVersion {
		return order, fmt.Errorf("%w: work order cannot be assigned", domain.ErrConflict)
	}
	if strings.TrimSpace(technician) == "" {
		return order, fmt.Errorf("%w: technician", domain.ErrInvalid)
	}
	out := order
	out.AssignedTo = technician
	out.Status = "assigned"
	out.Version++
	return out, nil
}

func Start(order WorkOrder, asset Asset, actor string, at time.Time, expectedOrder, expectedAsset int64) (WorkOrder, Asset, error) {
	if order.Status != "assigned" || order.AssignedTo != actor || order.Version != expectedOrder || asset.Version != expectedAsset {
		return order, asset, fmt.Errorf("%w: start versions or assignment", domain.ErrConflict)
	}
	if order.AssetID != asset.ID || order.TenantID != asset.TenantID {
		return order, asset, fmt.Errorf("%w: work order scope", domain.ErrConflict)
	}
	if at.Before(order.ScheduledWindow.StartsAt.Add(-15*time.Minute)) || at.After(order.ScheduledWindow.EndsAt) {
		return order, asset, fmt.Errorf("%w: start outside service window", domain.ErrConflict)
	}
	if asset.Status == AssetRetired || asset.Status == AssetServicing {
		return order, asset, fmt.Errorf("%w: asset unavailable", domain.ErrConflict)
	}
	outOrder := order
	outOrder.Status = "in_progress"
	outOrder.StartedAt = &at
	outOrder.Version++
	outAsset := asset
	outAsset.Status = AssetServicing
	outAsset.Version++
	return outOrder, outAsset, nil
}

func Complete(order WorkOrder, asset Asset, report ServiceReport, expectedOrder, expectedAsset int64) (WorkOrder, Asset, error) {
	if order.Status != "in_progress" || order.Version != expectedOrder || asset.Version != expectedAsset {
		return order, asset, fmt.Errorf("%w: completion versions or state", domain.ErrConflict)
	}
	if report.WorkOrderID != order.ID || report.AssetID != asset.ID || report.Technician != order.AssignedTo {
		return order, asset, fmt.Errorf("%w: service report scope", domain.ErrConflict)
	}
	if strings.TrimSpace(report.Resolution) == "" || report.LaborMins <= 0 || report.LaborMins > 24*60 {
		return order, asset, fmt.Errorf("%w: service report details", domain.ErrInvalid)
	}
	if order.StartedAt == nil || report.CompletedAt.Before(*order.StartedAt) {
		return order, asset, fmt.Errorf("%w: service completion time", domain.ErrInvalid)
	}
	if report.MeterHours < order.MeterAtOpen || report.MeterHours < asset.MeterHours {
		return order, asset, fmt.Errorf("%w: service meter rollback", domain.ErrConflict)
	}
	if err := validateParts(report.Parts); err != nil {
		return order, asset, err
	}
	outOrder := order
	outOrder.Status = "completed"
	outOrder.CompletedAt = &report.CompletedAt
	outOrder.Version++
	outAsset := asset
	outAsset.Status = AssetOperational
	outAsset.MeterHours = report.MeterHours
	outAsset.LastServiceHour = report.MeterHours
	outAsset.Version++
	return outOrder, outAsset, nil
}

func Cancel(order WorkOrder, asset Asset, reason string, expectedOrder, expectedAsset int64) (WorkOrder, Asset, error) {
	if order.Version != expectedOrder || asset.Version != expectedAsset {
		return order, asset, fmt.Errorf("%w: cancellation version", domain.ErrConflict)
	}
	if order.Status == "completed" || order.Status == "cancelled" || order.Status == "in_progress" {
		return order, asset, fmt.Errorf("%w: cannot cancel %s order", domain.ErrConflict, order.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return order, asset, fmt.Errorf("%w: cancellation reason", domain.ErrInvalid)
	}
	outOrder := order
	outOrder.Status = "cancelled"
	outOrder.Version++
	outAsset := asset
	if outAsset.Status == AssetDegraded {
		if due, _ := outAsset.ServiceDue(); !due {
			outAsset.Status = AssetOperational
		}
	}
	outAsset.Version++
	return outOrder, outAsset, nil
}

func validateParts(parts []Part) error {
	seenCodes := map[string]struct{}{}
	seenSerials := map[string]struct{}{}
	for _, part := range parts {
		if part.Code == "" || part.Name == "" || part.Quantity <= 0 || part.UnitCost < 0 {
			return fmt.Errorf("%w: service part", domain.ErrInvalid)
		}
		if _, exists := seenCodes[part.Code]; exists {
			return fmt.Errorf("%w: duplicate service part %s", domain.ErrConflict, part.Code)
		}
		seenCodes[part.Code] = struct{}{}
		if part.Serialized && len(part.Serials) != part.Quantity {
			return fmt.Errorf("%w: serialized part count", domain.ErrInvalid)
		}
		for _, serial := range part.Serials {
			if serial == "" {
				return fmt.Errorf("%w: empty part serial", domain.ErrInvalid)
			}
			if _, exists := seenSerials[serial]; exists {
				return fmt.Errorf("%w: duplicate part serial %s", domain.ErrConflict, serial)
			}
			seenSerials[serial] = struct{}{}
		}
	}
	return nil
}

func RankBacklog(orders []WorkOrder, now time.Time) []WorkOrder {
	backlog := make([]WorkOrder, 0, len(orders))
	for _, order := range orders {
		if order.Status == "completed" || order.Status == "cancelled" {
			continue
		}
		backlog = append(backlog, order)
	}
	sort.SliceStable(backlog, func(i, j int) bool {
		leftOverdue := now.After(backlog[i].ScheduledWindow.EndsAt)
		rightOverdue := now.After(backlog[j].ScheduledWindow.EndsAt)
		if leftOverdue != rightOverdue {
			return leftOverdue
		}
		if backlog[i].Priority != backlog[j].Priority {
			return backlog[i].Priority > backlog[j].Priority
		}
		if backlog[i].ScheduledWindow.StartsAt.Equal(backlog[j].ScheduledWindow.StartsAt) {
			return backlog[i].ID < backlog[j].ID
		}
		return backlog[i].ScheduledWindow.StartsAt.Before(backlog[j].ScheduledWindow.StartsAt)
	})
	return backlog
}

func CalculateDowntime(events []Downtime, window Window) (time.Duration, error) {
	if !window.EndsAt.After(window.StartsAt) {
		return 0, fmt.Errorf("%w: report window", domain.ErrInvalid)
	}
	clipped := make([]Downtime, 0, len(events))
	for _, event := range events {
		if !event.EndsAt.After(event.StartsAt) {
			return 0, fmt.Errorf("%w: downtime event", domain.ErrInvalid)
		}
		start := event.StartsAt
		if start.Before(window.StartsAt) {
			start = window.StartsAt
		}
		end := event.EndsAt
		if end.After(window.EndsAt) {
			end = window.EndsAt
		}
		if end.After(start) {
			clipped = append(clipped, Downtime{AssetID: event.AssetID, StartsAt: start, EndsAt: end, Reason: event.Reason})
		}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].StartsAt.Before(clipped[j].StartsAt) })
	var total time.Duration
	var current Window
	for _, event := range clipped {
		if current.StartsAt.IsZero() {
			current = Window{StartsAt: event.StartsAt, EndsAt: event.EndsAt}
			continue
		}
		if !event.StartsAt.After(current.EndsAt) {
			if event.EndsAt.After(current.EndsAt) {
				current.EndsAt = event.EndsAt
			}
			continue
		}
		total += current.EndsAt.Sub(current.StartsAt)
		current = Window{StartsAt: event.StartsAt, EndsAt: event.EndsAt}
	}
	if !current.StartsAt.IsZero() {
		total += current.EndsAt.Sub(current.StartsAt)
	}
	return total, nil
}
