package ingest

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

type observedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type takeResult struct {
	batch []model.Span
	err   error
}

func TestBufferRejectsWholeBatchWhenCapacityWouldBeExceeded(t *testing.T) {
	tracker := diagnostics.NewTracker()
	buffer := NewBuffer(2, tracker)

	if err := buffer.Offer([]model.Span{{SpanID: "1"}}); err != nil {
		t.Fatal(err)
	}
	err := buffer.Offer([]model.Span{{SpanID: "2"}, {SpanID: "3"}})
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("got %v", err)
	}

	batch, err := buffer.Take(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].SpanID != "1" {
		t.Fatalf("partial batch was accepted: %#v", batch)
	}
	if !tracker.Snapshot().IntegrityFailed {
		t.Fatal("overload must invalidate the execution")
	}
}

func TestBufferDrainsBeforeClosed(t *testing.T) {
	buffer := NewBuffer(2, diagnostics.NewTracker())
	if err := buffer.Offer([]model.Span{{SpanID: "1"}}); err != nil {
		t.Fatal(err)
	}
	buffer.Close()
	batch, err := buffer.Take(context.Background(), 2)
	if err != nil || len(batch) != 1 {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	if _, err := buffer.Take(context.Background(), 2); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestBufferTakeHonorsContext(t *testing.T) {
	buffer := NewBuffer(1, diagnostics.NewTracker())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buffer.Take(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBufferConcurrentOfferAndTake(t *testing.T) {
	tracker := diagnostics.NewTracker()
	buffer := NewBuffer(1000, tracker)
	const total = 500
	done := make(chan error, 1)
	go func() {
		received := 0
		for received < total {
			batch, err := buffer.Take(context.Background(), 17)
			if err != nil {
				done <- err
				return
			}
			received += len(batch)
		}
		done <- nil
	}()
	for index := 0; index < total; index++ {
		if err := buffer.Offer([]model.Span{{SpanID: strconv.Itoa(index)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	buffer.Close()
	if tracker.Snapshot().Received != total {
		t.Fatalf("got %d", tracker.Snapshot().Received)
	}
}

func TestBufferOfferWakesAllWaitingConsumersForAvailableSpans(t *testing.T) {
	buffer := NewBuffer(2, diagnostics.NewTracker())
	results := make(chan takeResult, 2)
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		observed := &observedContext{Context: ctx, waiting: make(chan struct{})}
		go func() {
			batch, err := buffer.Take(observed, 1)
			results <- takeResult{batch: batch, err: err}
		}()
		<-observed.waiting
	}

	if err := buffer.Offer([]model.Span{{SpanID: "1"}, {SpanID: "2"}}); err != nil {
		t.Fatal(err)
	}

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	seen := make(map[string]bool)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil || len(result.batch) != 1 {
				t.Fatalf("batch=%#v err=%v", result.batch, result.err)
			}
			seen[result.batch[0].SpanID] = true
		case <-deadline.C:
			t.Fatal("not all waiting consumers were woken for available spans")
		}
	}
	if !seen["1"] || !seen["2"] {
		t.Fatalf("seen spans: %#v", seen)
	}
}

func TestBufferCloseWakesAllWaitingConsumers(t *testing.T) {
	buffer := NewBuffer(1, diagnostics.NewTracker())
	results := make(chan error, 3)
	for range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		observed := &observedContext{Context: ctx, waiting: make(chan struct{})}
		go func() {
			_, err := buffer.Take(observed, 1)
			results <- err
		}()
		<-observed.waiting
	}

	buffer.Close()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for range 3 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("got %v", err)
			}
		case <-deadline.C:
			t.Fatal("not all waiting consumers were woken by close")
		}
	}
}
