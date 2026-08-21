package batch

import (
	"context"
	"fmt"
	"time"

	"go-base/internal/domain"
)

type indexedItem[T any] struct {
	index int
	item  Item[T]
}

type indexedResult[T any] struct {
	index  int
	result Result[T]
}

type itemExecutor[I, O any] struct {
	processor Processor[I, O]
	timeout   time.Duration
}

func (executor itemExecutor[I, O]) run(ctx context.Context, job indexedItem[I]) (out indexedResult[O]) {
	started := time.Now().UTC()
	result := Result[O]{
		Key:       job.item.Key,
		StartedAt: started,
	}
	defer func() {
		if r := recover(); r != nil {
			result.Succeeded = false
			result.Code = errorCode(domain.ErrInternal)
			result.Message = fmt.Sprintf("batch processor panic: %v", r)
			result.EndedAt = time.Now().UTC()
		}
		out = indexedResult[O]{index: job.index, result: result}
	}()

	itemContext, cancel := executor.itemContext(ctx)
	defer cancel()

	value, err := executor.processor(itemContext, job.item.Value)
	result.Value = value
	result.EndedAt = time.Now().UTC()
	if err == nil {
		result.Succeeded = true
		result.Code = "ok"
	} else {
		result.Code = errorCode(err)
		result.Message = err.Error()
	}
	return indexedResult[O]{index: job.index, result: result}
}

func (executor itemExecutor[I, O]) itemContext(parent context.Context) (context.Context, context.CancelFunc) {
	if executor.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, executor.timeout)
}

func canceledResult[T any](index int, key string, cause error) indexedResult[T] {
	now := time.Now().UTC()
	message := "batch stopped before item execution"
	if cause != nil {
		message = cause.Error()
	}
	return indexedResult[T]{
		index: index,
		result: Result[T]{
			Key:       key,
			Code:      "canceled",
			Message:   message,
			StartedAt: now,
			EndedAt:   now,
		},
	}
}
