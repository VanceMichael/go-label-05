package compliance

import (
	"fmt"
	"go-base/internal/domain"
	"sort"
	"time"
)

type Check struct {
	Code, Name   string
	Required     bool
	Passed       bool
	EvidenceID   string
	CheckedAt    time.Time
	ExpiresAfter time.Duration
}
type Review struct {
	BatchID, TenantID, ReviewerID string
	Checks                        []Check
	StartedAt, SubmittedAt        time.Time
}
type Result struct {
	Allowed                  bool
	Missing, Failed, Expired []string
}

func (r Review) Evaluate(now time.Time) (Result, error) {
	if r.BatchID == "" || r.ReviewerID == "" {
		return Result{}, fmt.Errorf("%w: review identity", domain.ErrInvalid)
	}
	if len(r.Checks) == 0 {
		return Result{}, fmt.Errorf("%w: empty review", domain.ErrInvalid)
	}
	result := Result{Allowed: true}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if c.Code == "" || seen[c.Code] {
			return Result{}, fmt.Errorf("%w: duplicate check", domain.ErrInvalid)
		}
		seen[c.Code] = true
		if c.Required && c.EvidenceID == "" {
			result.Missing = append(result.Missing, c.Code)
			result.Allowed = false
			continue
		}
		if !c.Passed {
			result.Failed = append(result.Failed, c.Code)
			result.Allowed = false
		}
		if c.ExpiresAfter > 0 && now.After(c.CheckedAt.Add(c.ExpiresAfter)) {
			result.Expired = append(result.Expired, c.Code)
			result.Allowed = false
		}
	}
	sort.Strings(result.Missing)
	sort.Strings(result.Failed)
	sort.Strings(result.Expired)
	return result, nil
}
func Merge(left, right Review) (Review, error) {
	if left.BatchID != right.BatchID || left.TenantID != right.TenantID {
		return Review{}, fmt.Errorf("%w: review scope", domain.ErrConflict)
	}
	out := left
	byCode := map[string]Check{}
	for _, c := range left.Checks {
		byCode[c.Code] = c
	}
	for _, c := range right.Checks {
		if old, ok := byCode[c.Code]; !ok || c.CheckedAt.After(old.CheckedAt) {
			byCode[c.Code] = c
		}
	}
	out.Checks = out.Checks[:0]
	for _, c := range byCode {
		out.Checks = append(out.Checks, c)
	}
	sort.Slice(out.Checks, func(i, j int) bool { return out.Checks[i].Code < out.Checks[j].Code })
	return out, nil
}
