package storage

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("storage queue full")

type QueueMetrics struct {
	Submitted, Committed, Rejected uint64
	TotalWait                      time.Duration
	MaxWait                        time.Duration
}
type queuedEvent struct {
	ctx      context.Context
	event    Event
	result   chan error
	enqueued time.Time
}

// QueuedWriter provides bounded backpressure around a Store for validation.
type QueuedWriter struct {
	store     Store
	queue     chan queuedEvent
	wg        sync.WaitGroup
	mu        sync.Mutex
	metrics   QueueMetrics
	closeOnce sync.Once
	closeErr  error
}

func NewQueuedWriter(store Store, capacity int) *QueuedWriter {
	if capacity < 1 {
		capacity = 1
	}
	w := &QueuedWriter{store: store, queue: make(chan queuedEvent, capacity)}
	w.wg.Add(1)
	go w.run()
	return w
}

func (w *QueuedWriter) Submit(ctx context.Context, event Event) error {
	req := queuedEvent{ctx: ctx, event: event, result: make(chan error, 1), enqueued: time.Now()}
	select {
	case w.queue <- req:
		w.mu.Lock()
		w.metrics.Submitted++
		w.mu.Unlock()
	case <-ctx.Done():
		w.mu.Lock()
		w.metrics.Rejected++
		w.mu.Unlock()
		return ctx.Err()
	}
	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *QueuedWriter) run() {
	defer w.wg.Done()
	for req := range w.queue {
		wait := time.Since(req.enqueued)
		w.mu.Lock()
		w.metrics.TotalWait += wait
		if wait > w.metrics.MaxWait {
			w.metrics.MaxWait = wait
		}
		w.mu.Unlock()
		err := w.store.Append(req.ctx, req.event)
		w.mu.Lock()
		if err == nil {
			w.metrics.Committed++
		}
		w.mu.Unlock()
		req.result <- err
	}
}

func (w *QueuedWriter) Metrics() QueueMetrics { w.mu.Lock(); defer w.mu.Unlock(); return w.metrics }
func (w *QueuedWriter) Close() error {
	w.closeOnce.Do(func() {
		close(w.queue)
		w.wg.Wait()
		w.closeErr = w.store.Close()
	})
	return w.closeErr
}
