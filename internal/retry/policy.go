package retry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-base/internal/domain"
)

type Policy struct {
	MaxAttempts int
	Initial     time.Duration
	Maximum     time.Duration
	Multiplier  float64
	Retryable   func(error) bool
	Sleep       func(context.Context, time.Duration) error
}

type Attempt struct {
	Number    int
	StartedAt time.Time
	EndedAt   time.Time
	Delay     time.Duration
	Error     string
}

type Result[T any] struct {
	Value    T
	Attempts []Attempt
	Elapsed  time.Duration
}

func Do[T any](ctx context.Context, policy Policy, operation func(context.Context) (T, error)) (Result[T], error) {
	var zero T
	if operation == nil || policy.MaxAttempts < 1 || policy.MaxAttempts > 20 || policy.Initial < 0 || policy.Maximum < policy.Initial || policy.Multiplier < 1 {
		return Result[T]{}, fmt.Errorf("%w: retry policy", domain.ErrInvalid)
	}
	if policy.Retryable == nil {
		policy.Retryable = func(error) bool { return false }
	}
	if policy.Sleep == nil {
		policy.Sleep = sleep
	}
	started := time.Now()
	result := Result[T]{}
	delay := policy.Initial
	for number := 1; number <= policy.MaxAttempts; number++ {
		attempt := Attempt{Number: number, StartedAt: time.Now()}
		value, err := operation(ctx)
		attempt.EndedAt = time.Now()
		if err == nil {
			result.Value = value
			result.Attempts = append(result.Attempts, attempt)
			result.Elapsed = time.Since(started)
			return result, nil
		}
		attempt.Error = err.Error()
		if ctx.Err() != nil {
			result.Attempts = append(result.Attempts, attempt)
			result.Elapsed = time.Since(started)
			return result, errors.Join(domain.ErrCanceled, ctx.Err())
		}
		if !policy.Retryable(err) || number == policy.MaxAttempts {
			result.Attempts = append(result.Attempts, attempt)
			result.Elapsed = time.Since(started)
			return result, err
		}
		attempt.Delay = delay
		result.Attempts = append(result.Attempts, attempt)
		if err := policy.Sleep(ctx, delay); err != nil {
			result.Elapsed = time.Since(started)
			return result, errors.Join(domain.ErrCanceled, err)
		}
		delay = nextDelay(delay, policy.Maximum, policy.Multiplier)
	}
	result.Value = zero
	return result, fmt.Errorf("%w: retry loop exhausted", domain.ErrConflict)
}

func nextDelay(current, maximum time.Duration, multiplier float64) time.Duration {
	next := time.Duration(float64(current) * multiplier)
	if next < current || next > maximum {
		return maximum
	}
	return next
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
