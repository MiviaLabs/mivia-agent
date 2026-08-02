package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestQueuedWriterSubmitCancelledBeforeEnqueue(t *testing.T) {
	store := &gatedStore{Store: NewMemory(), started: make(chan struct{}), release: make(chan struct{})}
	writer := NewQueuedWriter(store, 1)
	first := make(chan error, 1)
	go func() {
		first <- writer.Submit(context.Background(), Event{ID: "first", RunID: "run", Sequence: 1, Kind: "agent", Payload: []byte("safe")})
	}()
	<-store.started
	writer.queue <- queuedEvent{ctx: context.Background(), event: Event{ID: "queued", RunID: "run", Sequence: 2, Kind: "agent", Payload: []byte("safe")}, result: make(chan error, 1), enqueued: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.Submit(ctx, Event{ID: "cancelled", RunID: "run", Sequence: 3, Kind: "agent", Payload: []byte("safe")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit error = %v, want context.Canceled", err)
	}
	close(store.release)
	if err := <-first; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	metrics := writer.Metrics()
	if metrics.Submitted != 1 || metrics.Rejected != 1 || metrics.Committed != 2 {
		t.Fatalf("metrics = %+v", metrics)
	}
	if count, err := store.Count(context.Background()); err != nil || count != 2 {
		t.Fatalf("stored events = %d, %v; want 2, nil", count, err)
	}
}

type gatedStore struct {
	Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedStore) Append(ctx context.Context, event Event) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.Append(ctx, event)
}

func TestQueuedWriter_BoundedBackpressureAndMetrics(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	gated := &gatedStore{Store: s, started: make(chan struct{}), release: make(chan struct{})}
	w := NewQueuedWriter(gated, 2)
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- w.Submit(context.Background(), Event{ID: "queue-" + itoa(i), RunID: "queue", Sequence: i + 1, Kind: "agent", Payload: []byte("safe")})
		}(i)
	}
	<-gated.started
	close(gated.release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	m := w.Metrics()
	if m.Submitted != 20 || m.Committed != 20 {
		t.Fatalf("metrics=%+v", m)
	}
	if m.MaxWait <= 0 {
		t.Fatalf("expected queue wait, metrics=%+v", m)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
