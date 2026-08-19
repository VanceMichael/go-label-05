package compliance

import (
	"testing"
	"time"
)

func TestReviewEvaluate(t *testing.T) {
	r := Review{BatchID: "b", ReviewerID: "u", Checks: []Check{{Code: "odor", Required: true, Passed: true, EvidenceID: "e", CheckedAt: time.Now()}, {Code: "water", Required: true, Passed: false, EvidenceID: "e2", CheckedAt: time.Now()}}}
	out, err := r.Evaluate(time.Now())
	if err != nil || out.Allowed || len(out.Failed) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestMergeReview(t *testing.T) {
	n := time.Now()
	a := Review{BatchID: "b", TenantID: "t", Checks: []Check{{Code: "a", CheckedAt: n}}}
	b := Review{BatchID: "b", TenantID: "t", Checks: []Check{{Code: "b", CheckedAt: n}}}
	out, err := Merge(a, b)
	if err != nil || len(out.Checks) != 2 {
		t.Fatalf("%+v %v", out, err)
	}
}
