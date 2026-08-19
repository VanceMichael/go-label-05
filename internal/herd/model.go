package herd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"go-base/internal/domain"
)

type Lifecycle string

const (
	LifecycleActive      Lifecycle = "active"
	LifecycleQuarantine  Lifecycle = "quarantine"
	LifecycleDry         Lifecycle = "dry"
	LifecycleTransferred Lifecycle = "transferred"
)

type Animal struct {
	ID        string
	TenantID  string
	GroupID   string
	EarTag    string
	Lifecycle Lifecycle
	BirthDate time.Time
	WeightKg  float64
	Version   int64
	UpdatedAt time.Time
}

type Group struct {
	ID        string
	TenantID  string
	BarnID    string
	Name      string
	Lifecycle Lifecycle
	Animals   []Animal
	Capacity  int
	Version   int64
}

type HealthKind string

const (
	HealthTemperature HealthKind = "temperature"
	HealthRumination  HealthKind = "rumination"
	HealthMobility    HealthKind = "mobility"
	HealthTreatment   HealthKind = "treatment"
)

type HealthEvent struct {
	ID         string
	TenantID   string
	AnimalID   string
	Kind       HealthKind
	Value      float64
	Unit       string
	ObservedAt time.Time
	RecordedAt time.Time
	ActorID    string
	Notes      string
}

type HealthAssessment struct {
	AnimalID        string
	Risk            string
	Reasons         []string
	LatestObserved  time.Time
	TemperatureC    *float64
	RuminationMins  *float64
	MobilityScore   *float64
	TreatmentActive bool
}

type Transfer struct {
	ID          string
	TenantID    string
	AnimalID    string
	FromGroupID string
	ToGroupID   string
	RequestedBy string
	Reason      string
	RequestedAt time.Time
	CompletedAt *time.Time
	Status      string
	Version     int64
}

func (a Animal) Validate(now time.Time) error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.TenantID) == "" {
		return fmt.Errorf("%w: animal identity", domain.ErrInvalid)
	}
	if strings.TrimSpace(a.GroupID) == "" || strings.TrimSpace(a.EarTag) == "" {
		return fmt.Errorf("%w: animal assignment", domain.ErrInvalid)
	}
	if a.BirthDate.IsZero() || a.BirthDate.After(now) {
		return fmt.Errorf("%w: animal birth date", domain.ErrInvalid)
	}
	if a.WeightKg < 20 || a.WeightKg > 1500 {
		return fmt.Errorf("%w: animal weight", domain.ErrInvalid)
	}
	switch a.Lifecycle {
	case LifecycleActive, LifecycleQuarantine, LifecycleDry, LifecycleTransferred:
		return nil
	default:
		return fmt.Errorf("%w: animal lifecycle", domain.ErrInvalid)
	}
}

func (g Group) Validate(now time.Time) error {
	if g.ID == "" || g.TenantID == "" || g.BarnID == "" || g.Name == "" {
		return fmt.Errorf("%w: group identity", domain.ErrInvalid)
	}
	if g.Capacity <= 0 {
		return fmt.Errorf("%w: group capacity", domain.ErrInvalid)
	}
	active := 0
	tags := make(map[string]struct{}, len(g.Animals))
	for _, animal := range g.Animals {
		if err := animal.Validate(now); err != nil {
			return fmt.Errorf("group %s: %w", g.ID, err)
		}
		if animal.TenantID != g.TenantID || animal.GroupID != g.ID {
			return fmt.Errorf("%w: animal outside group scope", domain.ErrConflict)
		}
		if _, exists := tags[animal.EarTag]; exists {
			return fmt.Errorf("%w: duplicate ear tag %s", domain.ErrConflict, animal.EarTag)
		}
		tags[animal.EarTag] = struct{}{}
		if animal.Lifecycle != LifecycleTransferred {
			active++
		}
	}
	if active > g.Capacity {
		return fmt.Errorf("%w: group capacity exceeded", domain.ErrConflict)
	}
	return nil
}

func (e HealthEvent) Validate(now time.Time) error {
	if e.ID == "" || e.TenantID == "" || e.AnimalID == "" || e.ActorID == "" {
		return fmt.Errorf("%w: health event identity", domain.ErrInvalid)
	}
	if e.ObservedAt.After(now.Add(2 * time.Minute)) {
		return fmt.Errorf("%w: health event is in the future", domain.ErrInvalid)
	}
	if now.Sub(e.ObservedAt) > 14*24*time.Hour {
		return fmt.Errorf("%w: health event is too old", domain.ErrInvalid)
	}
	switch e.Kind {
	case HealthTemperature:
		if e.Unit != "C" || e.Value < 30 || e.Value > 45 {
			return fmt.Errorf("%w: temperature measurement", domain.ErrInvalid)
		}
	case HealthRumination:
		if e.Unit != "minutes" || e.Value < 0 || e.Value > 1440 {
			return fmt.Errorf("%w: rumination measurement", domain.ErrInvalid)
		}
	case HealthMobility:
		if e.Unit != "score" || e.Value < 0 || e.Value > 5 {
			return fmt.Errorf("%w: mobility measurement", domain.ErrInvalid)
		}
	case HealthTreatment:
		if e.Unit != "active" || (e.Value != 0 && e.Value != 1) {
			return fmt.Errorf("%w: treatment state", domain.ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: health event kind", domain.ErrInvalid)
	}
	return nil
}

func Assess(animal Animal, events []HealthEvent, now time.Time) (HealthAssessment, error) {
	if err := animal.Validate(now); err != nil {
		return HealthAssessment{}, err
	}
	relevant := make([]HealthEvent, 0, len(events))
	for _, event := range events {
		if event.AnimalID != animal.ID || event.TenantID != animal.TenantID {
			continue
		}
		if err := event.Validate(now); err != nil {
			return HealthAssessment{}, err
		}
		relevant = append(relevant, event)
	}
	sort.SliceStable(relevant, func(i, j int) bool {
		if relevant[i].ObservedAt.Equal(relevant[j].ObservedAt) {
			return relevant[i].ID < relevant[j].ID
		}
		return relevant[i].ObservedAt.Before(relevant[j].ObservedAt)
	})
	result := HealthAssessment{AnimalID: animal.ID, Risk: "normal"}
	seen := map[HealthKind]bool{}
	for i := len(relevant) - 1; i >= 0; i-- {
		event := relevant[i]
		if event.ObservedAt.Before(now.Add(-48*time.Hour)) || seen[event.Kind] {
			continue
		}
		seen[event.Kind] = true
		if event.ObservedAt.After(result.LatestObserved) {
			result.LatestObserved = event.ObservedAt
		}
		value := event.Value
		switch event.Kind {
		case HealthTemperature:
			result.TemperatureC = &value
			if value >= 40.5 {
				result.Risk = "critical"
				result.Reasons = append(result.Reasons, "temperature at or above 40.5 C")
			} else if value >= 39.5 {
				result.Risk = maxRisk(result.Risk, "watch")
				result.Reasons = append(result.Reasons, "temperature above normal range")
			}
		case HealthRumination:
			result.RuminationMins = &value
			if value < 300 {
				result.Risk = maxRisk(result.Risk, "watch")
				result.Reasons = append(result.Reasons, "rumination below 300 minutes")
			}
		case HealthMobility:
			result.MobilityScore = &value
			if value >= 4 {
				result.Risk = "critical"
				result.Reasons = append(result.Reasons, "mobility score indicates severe lameness")
			} else if value >= 3 {
				result.Risk = maxRisk(result.Risk, "watch")
				result.Reasons = append(result.Reasons, "mobility score requires review")
			}
		case HealthTreatment:
			result.TreatmentActive = value == 1
		}
	}
	if animal.Lifecycle == LifecycleQuarantine {
		result.Risk = maxRisk(result.Risk, "watch")
		result.Reasons = append(result.Reasons, "animal is quarantined")
	}
	if len(relevant) == 0 {
		result.Risk = "unknown"
		result.Reasons = append(result.Reasons, "no recent observations")
	}
	return result, nil
}

func NewTransfer(id string, animal Animal, targetGroup, actor, reason string, at time.Time) (Transfer, error) {
	if animal.Lifecycle == LifecycleTransferred {
		return Transfer{}, fmt.Errorf("%w: animal already transferred", domain.ErrConflict)
	}
	if targetGroup == "" || targetGroup == animal.GroupID || actor == "" || strings.TrimSpace(reason) == "" {
		return Transfer{}, fmt.Errorf("%w: transfer request", domain.ErrInvalid)
	}
	return Transfer{ID: id, TenantID: animal.TenantID, AnimalID: animal.ID, FromGroupID: animal.GroupID, ToGroupID: targetGroup, RequestedBy: actor, Reason: strings.TrimSpace(reason), RequestedAt: at, Status: "pending", Version: 1}, nil
}

func CompleteTransfer(transfer Transfer, source, target Group, at time.Time) (Transfer, Group, Group, error) {
	if transfer.Status != "pending" || transfer.Version < 1 {
		return transfer, source, target, fmt.Errorf("%w: transfer is not pending", domain.ErrConflict)
	}
	if transfer.TenantID != source.TenantID || transfer.TenantID != target.TenantID || transfer.FromGroupID != source.ID || transfer.ToGroupID != target.ID {
		return transfer, source, target, fmt.Errorf("%w: transfer scope mismatch", domain.ErrConflict)
	}
	index := -1
	for i := range source.Animals {
		if source.Animals[i].ID == transfer.AnimalID {
			index = i
			break
		}
	}
	if index < 0 {
		return transfer, source, target, fmt.Errorf("%w: animal missing from source group", domain.ErrNotFound)
	}
	if len(target.Animals) >= target.Capacity {
		return transfer, source, target, fmt.Errorf("%w: target group is full", domain.ErrConflict)
	}
	newSource := source
	newSource.Animals = append([]Animal(nil), source.Animals...)
	animal := newSource.Animals[index]
	newSource.Animals = append(newSource.Animals[:index], newSource.Animals[index+1:]...)
	newSource.Version++
	newTarget := target
	newTarget.Animals = append([]Animal(nil), target.Animals...)
	animal.GroupID = target.ID
	animal.UpdatedAt = at
	animal.Version++
	newTarget.Animals = append(newTarget.Animals, animal)
	newTarget.Version++
	completed := transfer
	completed.Status = "completed"
	completed.CompletedAt = &at
	completed.Version++
	return completed, newSource, newTarget, nil
}

func maxRisk(current, next string) string {
	rank := map[string]int{"unknown": 0, "normal": 1, "watch": 2, "critical": 3}
	if rank[next] > rank[current] {
		return next
	}
	return current
}
