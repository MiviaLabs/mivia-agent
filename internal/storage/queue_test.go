package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

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
