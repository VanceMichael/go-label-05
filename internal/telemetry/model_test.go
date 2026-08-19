package telemetry

import (
	"testing"
	"time"
)

func TestAlarmThresholds(t *testing.T) {
	r := Reading{SensorID: "s", BarnID: "b", TenantID: "t", Kind: KindAmmonia, Value: 31, Sequence: 1, ObservedAt: time.Now(), ReceivedAt: time.Now()}
	a, ok := Evaluate(r, Threshold{Warning: 20, Critical: 30, HigherIsWorse: true})
	if !ok || a.Severity != "critical" {
		t.Fatalf("%+v %v", a, ok)
	}
}
func TestStableWindowCopiesAndSorts(t *testing.T) {
	now := time.Now()
	in := []Reading{{Sequence: 2, ObservedAt: now}, {Sequence: 1, ObservedAt: now.Add(-time.Minute)}}
	out := StableWindow(in, 2)
	if out[0].Sequence != 1 {
		t.Fatal(out)
	}
	out[0].Sequence = 99
	if in[1].Sequence == 99 {
		t.Fatal("shared backing storage")
	}
}
