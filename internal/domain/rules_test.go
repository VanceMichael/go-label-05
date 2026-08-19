package domain

import (
	"errors"
	"testing"
	"time"
)

func TestValidateTransitions(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		ok       bool
	}{{"draft", "scheduled", true}, {"scheduled", "completed", true}, {"draft", "completed", false}, {"completed", "draft", false}} {
		if err := ValidateTransition(tc.from, tc.to); (err == nil) != tc.ok {
			t.Fatalf("%s -> %s: %v", tc.from, tc.to, err)
		}
	}
}

func TestFeedPlanRejectsPastAndAcceptsFuture(t *testing.T) {
	now := time.Now()
	p := FeedPlan{TenantID: "t", GroupID: "g", OperatorID: "u", FeedKg: 10, ScheduledFor: now.Add(time.Hour)}
	if err := ValidateFeedPlan(p, now); err != nil {
		t.Fatal(err)
	}
	p.ScheduledFor = now.Add(-time.Hour)
	if !errors.Is(ValidateFeedPlan(p, now), ErrInvalid) {
		t.Fatal("past schedule accepted")
	}
}

func TestCompostOutput(t *testing.T) {
	v, err := CompostOutput(100, .25)
	if err != nil || v != 54 {
		t.Fatalf("%v %v", v, err)
	}
	if !errors.Is(func() error { _, e := CompostOutput(0, .2); return e }(), ErrInvalid) {
		t.Fatal("invalid weight accepted")
	}
}
