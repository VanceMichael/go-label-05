package herd

import (
	"errors"
	"testing"
	"time"

	"go-base/internal/domain"
)

func validAnimal(now time.Time) Animal {
	return Animal{ID: "animal-1", TenantID: "tenant-1", GroupID: "group-1", EarTag: "CN-1001", Lifecycle: LifecycleActive, BirthDate: now.AddDate(-2, 0, 0), WeightKg: 620, Version: 1, UpdatedAt: now}
}

func TestAnimalValidation(t *testing.T) {
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*Animal)
		ok   bool
	}{
		{"valid", func(*Animal) {}, true},
		{"missing tag", func(a *Animal) { a.EarTag = "" }, false},
		{"future birth", func(a *Animal) { a.BirthDate = now.Add(time.Hour) }, false},
		{"too light", func(a *Animal) { a.WeightKg = 10 }, false},
		{"too heavy", func(a *Animal) { a.WeightKg = 1600 }, false},
		{"unknown lifecycle", func(a *Animal) { a.Lifecycle = "lost" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			animal := validAnimal(now)
			test.edit(&animal)
			if err := animal.Validate(now); (err == nil) != test.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGroupValidationProtectsScopeCapacityAndTags(t *testing.T) {
	now := time.Now().UTC()
	first := validAnimal(now)
	group := Group{ID: "group-1", TenantID: "tenant-1", BarnID: "barn-1", Name: "Lactating", Lifecycle: LifecycleActive, Capacity: 2, Animals: []Animal{first}, Version: 1}
	if err := group.Validate(now); err != nil {
		t.Fatal(err)
	}
	wrongTenant := first
	wrongTenant.ID = "animal-2"
	wrongTenant.EarTag = "CN-1002"
	wrongTenant.TenantID = "tenant-2"
	group.Animals = append(group.Animals, wrongTenant)
	if !errors.Is(group.Validate(now), domain.ErrConflict) {
		t.Fatal("cross-tenant animal was accepted")
	}
	group.Animals[1].TenantID = "tenant-1"
	group.Animals[1].EarTag = first.EarTag
	if !errors.Is(group.Validate(now), domain.ErrConflict) {
		t.Fatal("duplicate ear tag was accepted")
	}
	group.Animals[1].EarTag = "CN-1002"
	group.Capacity = 1
	if !errors.Is(group.Validate(now), domain.ErrConflict) {
		t.Fatal("capacity violation was accepted")
	}
}

func TestHealthEventValidation(t *testing.T) {
	now := time.Now().UTC()
	base := HealthEvent{ID: "event-1", TenantID: "tenant-1", AnimalID: "animal-1", ActorID: "operator-1", ObservedAt: now.Add(-time.Hour), RecordedAt: now}
	tests := []struct {
		kind  HealthKind
		unit  string
		value float64
		ok    bool
	}{
		{HealthTemperature, "C", 39.2, true},
		{HealthTemperature, "F", 102, false},
		{HealthRumination, "minutes", 420, true},
		{HealthRumination, "minutes", 1500, false},
		{HealthMobility, "score", 4, true},
		{HealthMobility, "score", 6, false},
		{HealthTreatment, "active", 1, true},
		{HealthTreatment, "active", 2, false},
	}
	for _, test := range tests {
		event := base
		event.Kind, event.Unit, event.Value = test.kind, test.unit, test.value
		if err := event.Validate(now); (err == nil) != test.ok {
			t.Fatalf("kind=%s value=%v error=%v", test.kind, test.value, err)
		}
	}
}

func TestAssessmentUsesLatestMeasurementsAndDoesNotMutateEvents(t *testing.T) {
	now := time.Now().UTC()
	animal := validAnimal(now)
	events := []HealthEvent{
		{ID: "e1", TenantID: animal.TenantID, AnimalID: animal.ID, ActorID: "u", Kind: HealthTemperature, Unit: "C", Value: 40.8, ObservedAt: now.Add(-3 * time.Hour), RecordedAt: now},
		{ID: "e2", TenantID: animal.TenantID, AnimalID: animal.ID, ActorID: "u", Kind: HealthTemperature, Unit: "C", Value: 39.0, ObservedAt: now.Add(-time.Hour), RecordedAt: now},
		{ID: "e3", TenantID: animal.TenantID, AnimalID: animal.ID, ActorID: "u", Kind: HealthMobility, Unit: "score", Value: 4, ObservedAt: now.Add(-2 * time.Hour), RecordedAt: now},
	}
	assessment, err := Assess(animal, events, now)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Risk != "critical" || assessment.TemperatureC == nil || *assessment.TemperatureC != 39 || assessment.MobilityScore == nil || *assessment.MobilityScore != 4 {
		t.Fatalf("assessment=%+v", assessment)
	}
	if events[0].ID != "e1" || events[1].ID != "e2" {
		t.Fatal("assessment reordered caller slice")
	}
}

func TestAssessmentUnknownWithoutRelevantEvents(t *testing.T) {
	now := time.Now().UTC()
	animal := validAnimal(now)
	assessment, err := Assess(animal, []HealthEvent{{ID: "other", TenantID: animal.TenantID, AnimalID: "other", ActorID: "u", Kind: HealthTemperature, Unit: "C", Value: 40, ObservedAt: now, RecordedAt: now}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Risk != "unknown" || len(assessment.Reasons) != 1 {
		t.Fatalf("assessment=%+v", assessment)
	}
}

func TestCompleteTransferMovesOneAnimalWithoutSharingSlices(t *testing.T) {
	now := time.Now().UTC()
	animal := validAnimal(now)
	source := Group{ID: "group-1", TenantID: "tenant-1", BarnID: "barn-1", Name: "A", Capacity: 2, Animals: []Animal{animal}, Version: 3}
	target := Group{ID: "group-2", TenantID: "tenant-1", BarnID: "barn-1", Name: "B", Capacity: 2, Version: 7}
	transfer, err := NewTransfer("transfer-1", animal, target.ID, "manager-1", "health cohort change", now)
	if err != nil {
		t.Fatal(err)
	}
	done, newSource, newTarget, err := CompleteTransfer(transfer, source, target, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || len(newSource.Animals) != 0 || len(newTarget.Animals) != 1 || newTarget.Animals[0].GroupID != target.ID {
		t.Fatalf("done=%+v source=%+v target=%+v", done, newSource, newTarget)
	}
	newTarget.Animals[0].EarTag = "changed"
	if source.Animals[0].EarTag == "changed" {
		t.Fatal("transfer result shares animal storage with source")
	}
}

func TestCompleteTransferRejectsFullTargetAndPreservesInputs(t *testing.T) {
	now := time.Now().UTC()
	animal := validAnimal(now)
	other := animal
	other.ID, other.GroupID, other.EarTag = "animal-2", "group-2", "CN-1002"
	source := Group{ID: "group-1", TenantID: "tenant-1", Capacity: 1, Animals: []Animal{animal}}
	target := Group{ID: "group-2", TenantID: "tenant-1", Capacity: 1, Animals: []Animal{other}}
	transfer, _ := NewTransfer("transfer-1", animal, target.ID, "manager", "move", now)
	_, _, _, err := CompleteTransfer(transfer, source, target, now)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error=%v", err)
	}
	if len(source.Animals) != 1 || len(target.Animals) != 1 {
		t.Fatal("failed transfer mutated inputs")
	}
}
