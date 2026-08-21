package batch

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProcessorPanicIsContainedAndStopsRemainingWork(t *testing.T) {
	items := []Item[int]{
		{Key: "sensor-corrupt", Value: 1},
		{Key: "sensor-waiting", Value: 2},
	}
	var waitingCalls atomic.Int32

	report, err := Run(context.Background(), items, Options{
		Workers:            1,
		StopOnError:        true,
		PreserveInputOrder: true,
	}, func(_ context.Context, value int) (string, error) {
		if value == 1 {
			panic("telemetry decoder invariant violated")
		}
		waitingCalls.Add(1)
		return "processed", nil
	})
	if err != nil {
		t.Fatalf("Run() returned infrastructure error: %v", err)
	}
	if waitingCalls.Load() != 0 {
		t.Fatalf("processor called for work queued after panic: %d", waitingCalls.Load())
	}
	if len(report.Results) != 2 || report.Total != 2 {
		t.Fatalf("report size = %d total = %d", len(report.Results), report.Total)
	}
	failed := report.Results[0]
	if failed.Key != "sensor-corrupt" || failed.Succeeded || failed.Code != "internal" {
		t.Fatalf("panic result = %+v", failed)
	}
	if !strings.Contains(failed.Message, "telemetry decoder invariant violated") {
		t.Fatalf("panic message = %q", failed.Message)
	}
	canceled := report.Results[1]
	if canceled.Key != "sensor-waiting" || canceled.Succeeded || canceled.Code != "canceled" {
		t.Fatalf("queued result = %+v", canceled)
	}
	if report.Succeeded != 0 || report.Failed != 1 || report.Canceled != 1 {
		t.Fatalf("report counts = succeeded:%d failed:%d canceled:%d", report.Succeeded, report.Failed, report.Canceled)
	}
}

func TestBatchPreservesInputOrder(t *testing.T) {
	items := []Item[int]{
		{Key: "third", Value: 3},
		{Key: "first", Value: 1},
		{Key: "second", Value: 2},
	}
	report, err := Run(context.Background(), items, Options{
		Workers:            2,
		PreserveInputOrder: true,
	}, func(_ context.Context, value int) (int, error) {
		return value * 10, nil
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Results) != len(items) {
		t.Fatalf("result count = %d", len(report.Results))
	}
	for index, item := range items {
		if report.Results[index].Key != item.Key || report.Results[index].Value != item.Value*10 {
			t.Fatalf("result[%d] = %+v", index, report.Results[index])
		}
	}
}
