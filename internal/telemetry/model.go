package telemetry

import (
	"fmt"
	"go-base/internal/domain"
	"sort"
	"time"
)

type Kind string

const (
	KindTemperature Kind = "temperature"
	KindHumidity    Kind = "humidity"
	KindAmmonia     Kind = "ammonia"
	KindFeederFlow  Kind = "feeder_flow"
)

type Reading struct {
	SensorID, BarnID, TenantID string
	Kind                       Kind
	Value                      float64
	ObservedAt, ReceivedAt     time.Time
	Sequence                   int64
}
type Alarm struct {
	SensorID, BarnID, TenantID, Severity, Reason string
	Reading                                      Reading
	CreatedAt                                    time.Time
}
type Threshold struct {
	Warning, Critical float64
	HigherIsWorse     bool
}

func (r Reading) Validate(now time.Time) error {
	if r.SensorID == "" || r.BarnID == "" || r.TenantID == "" {
		return fmt.Errorf("%w: telemetry identity", domain.ErrInvalid)
	}
	if r.Sequence < 1 {
		return fmt.Errorf("%w: telemetry sequence", domain.ErrInvalid)
	}
	if r.ObservedAt.After(now.Add(2 * time.Minute)) {
		return fmt.Errorf("%w: future telemetry", domain.ErrInvalid)
	}
	if r.ReceivedAt.Sub(r.ObservedAt) > 30*time.Minute {
		return fmt.Errorf("%w: stale telemetry", domain.ErrInvalid)
	}
	return nil
}
func Evaluate(r Reading, t Threshold) (Alarm, bool) {
	severity := ""
	if t.HigherIsWorse {
		if r.Value >= t.Critical {
			severity = "critical"
		} else if r.Value >= t.Warning {
			severity = "warning"
		}
	} else {
		if r.Value <= t.Critical {
			severity = "critical"
		} else if r.Value <= t.Warning {
			severity = "warning"
		}
	}
	if severity == "" {
		return Alarm{}, false
	}
	return Alarm{SensorID: r.SensorID, BarnID: r.BarnID, TenantID: r.TenantID, Severity: severity, Reason: fmt.Sprintf("%s reading %.3f crossed threshold", r.Kind, r.Value), Reading: r, CreatedAt: r.ReceivedAt}, true
}
func StableWindow(readings []Reading, limit int) []Reading {
	copyOf := append([]Reading(nil), readings...)
	sort.SliceStable(copyOf, func(i, j int) bool {
		if copyOf[i].ObservedAt.Equal(copyOf[j].ObservedAt) {
			return copyOf[i].Sequence < copyOf[j].Sequence
		}
		return copyOf[i].ObservedAt.Before(copyOf[j].ObservedAt)
	})
	if limit > 0 && len(copyOf) > limit {
		copyOf = copyOf[len(copyOf)-limit:]
	}
	return copyOf
}
