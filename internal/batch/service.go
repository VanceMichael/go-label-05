package batch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go-base/internal/domain"
)

type Item[T any] struct {
	Key   string
	Value T
}

type Result[T any] struct {
	Key       string
	Value     T
	Succeeded bool
	Code      string
	Message   string
	StartedAt time.Time
	EndedAt   time.Time
}

type Report[T any] struct {
	Results   []Result[T]
	Total     int
	Succeeded int
	Failed    int
	Canceled  int
	StartedAt time.Time
	EndedAt   time.Time
}

type Processor[I, O any] func(context.Context, I) (O, error)

type Options struct {
	Workers            int
	StopOnError        bool
	PerItemTimeout     time.Duration
	PreserveInputOrder bool
}

func Run[I, O any](ctx context.Context, items []Item[I], options Options, processor Processor[I, O]) (Report[O], error) {
	if processor == nil {
		return Report[O]{}, fmt.Errorf("%w: batch processor", domain.ErrInvalid)
	}
	if options.Workers < 1 || options.Workers > 64 {
		return Report[O]{}, fmt.Errorf("%w: worker count", domain.ErrInvalid)
	}
	if options.PerItemTimeout < 0 {
		return Report[O]{}, fmt.Errorf("%w: per item timeout", domain.ErrInvalid)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Key == "" {
			return Report[O]{}, fmt.Errorf("%w: batch item key", domain.ErrInvalid)
		}
		if _, exists := seen[item.Key]; exists {
			return Report[O]{}, fmt.Errorf("%w: duplicate batch item %s", domain.ErrConflict, item.Key)
		}
		seen[item.Key] = struct{}{}
	}
	report := Report[O]{Total: len(items), StartedAt: time.Now().UTC()}
	if len(items) == 0 {
		report.EndedAt = report.StartedAt
		return report, nil
	}
	jobs := make(chan indexedItem[I])
	results := make(chan indexedResult[O], len(items))
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	executor := itemExecutor[I, O]{processor: processor, timeout: options.PerItemTimeout}
	var workers sync.WaitGroup
	for worker := 0; worker < options.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				result := executor.run(workerContext, job)
				if !result.result.Succeeded && options.StopOnError {
					cancel()
				}
				results <- result
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index, item := range items {
			select {
			case <-workerContext.Done():
				for rest := index; rest < len(items); rest++ {
					results <- canceledResult[O](rest, items[rest].Key, workerContext.Err())
				}
				return
			case jobs <- indexedItem[I]{index: index, item: item}:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	indexedResults := make([]indexedResult[O], 0, len(items))
	for result := range results {
		indexedResults = append(indexedResults, result)
		if result.result.Succeeded {
			report.Succeeded++
		} else if result.result.Code == "canceled" {
			report.Canceled++
		} else {
			report.Failed++
		}
	}
	if options.PreserveInputOrder {
		sort.Slice(indexedResults, func(i, j int) bool { return indexedResults[i].index < indexedResults[j].index })
	} else {
		sort.SliceStable(indexedResults, func(i, j int) bool {
			if indexedResults[i].result.EndedAt.Equal(indexedResults[j].result.EndedAt) {
				return indexedResults[i].result.Key < indexedResults[j].result.Key
			}
			return indexedResults[i].result.EndedAt.Before(indexedResults[j].result.EndedAt)
		})
	}
	for _, result := range indexedResults {
		report.Results = append(report.Results, result.result)
	}
	report.EndedAt = time.Now().UTC()
	if ctx.Err() != nil {
		return report, errors.Join(domain.ErrCanceled, ctx.Err())
	}
	return report, nil
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, domain.ErrCanceled):
		return "canceled"
	case errors.Is(err, domain.ErrInvalid):
		return "invalid"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrConflict):
		return "conflict"
	default:
		return "internal"
	}
}

func RetryFailed[I, O any](ctx context.Context, original []Item[I], prior Report[O], options Options, processor Processor[I, O]) (Report[O], error) {
	failed := make(map[string]struct{})
	for _, result := range prior.Results {
		if !result.Succeeded {
			failed[result.Key] = struct{}{}
		}
	}
	items := make([]Item[I], 0, len(failed))
	for _, item := range original {
		if _, exists := failed[item.Key]; exists {
			items = append(items, item)
		}
	}
	return Run(ctx, items, options, processor)
}

func MergeReports[T any](reports ...Report[T]) (Report[T], error) {
	merged := Report[T]{}
	byKey := make(map[string]Result[T])
	for _, report := range reports {
		if merged.StartedAt.IsZero() || (!report.StartedAt.IsZero() && report.StartedAt.Before(merged.StartedAt)) {
			merged.StartedAt = report.StartedAt
		}
		if report.EndedAt.After(merged.EndedAt) {
			merged.EndedAt = report.EndedAt
		}
		for _, result := range report.Results {
			if current, exists := byKey[result.Key]; exists && result.StartedAt.Before(current.StartedAt) {
				continue
			}
			byKey[result.Key] = result
		}
	}
	for _, result := range byKey {
		merged.Results = append(merged.Results, result)
		if result.Succeeded {
			merged.Succeeded++
		} else if result.Code == "canceled" {
			merged.Canceled++
		} else {
			merged.Failed++
		}
	}
	sort.Slice(merged.Results, func(i, j int) bool { return merged.Results[i].Key < merged.Results[j].Key })
	merged.Total = len(merged.Results)
	if merged.Total != merged.Succeeded+merged.Failed+merged.Canceled {
		return Report[T]{}, fmt.Errorf("%w: inconsistent batch report", domain.ErrConflict)
	}
	return merged, nil
}
