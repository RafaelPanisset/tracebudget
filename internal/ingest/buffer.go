package ingest

import (
	"context"
	"errors"
	"sync"

	"github.com/RafaelPanisset/tracebudget/internal/diagnostics"
	"github.com/RafaelPanisset/tracebudget/internal/model"
)

var (
	ErrClosed     = errors.New("span buffer closed")
	ErrOverloaded = errors.New("span buffer capacity exceeded")
)

type Buffer struct {
	mu      sync.Mutex
	queue   []model.Span
	limit   int
	closed  bool
	wake    chan struct{}
	tracker *diagnostics.Tracker
}

func NewBuffer(limit int, tracker *diagnostics.Tracker) *Buffer {
	return &Buffer{limit: limit, wake: make(chan struct{}), tracker: tracker}
}

func (b *Buffer) Offer(batch []model.Span) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	if len(b.queue)+len(batch) > b.limit {
		b.tracker.MarkIntegrityFailure("ingestion buffer overloaded")
		return ErrOverloaded
	}
	b.queue = append(b.queue, append([]model.Span(nil), batch...)...)
	b.tracker.AddReceived(len(batch))
	b.tracker.SetBufferDepth(len(b.queue))
	close(b.wake)
	b.wake = make(chan struct{})
	return nil
}

func (b *Buffer) Take(ctx context.Context, maximum int) ([]model.Span, error) {
	for {
		b.mu.Lock()
		if len(b.queue) > 0 {
			count := maximum
			if count > len(b.queue) {
				count = len(b.queue)
			}
			result := append([]model.Span(nil), b.queue[:count]...)
			b.queue = append(b.queue[:0], b.queue[count:]...)
			b.tracker.SetBufferDepth(len(b.queue))
			b.mu.Unlock()
			return result, nil
		}
		if b.closed {
			b.mu.Unlock()
			return nil, ErrClosed
		}
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (b *Buffer) Close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.wake)
	}
	b.mu.Unlock()
}
