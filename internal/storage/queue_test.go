package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestQueuedWriter_BoundedBackpressureAndMetrics(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := NewQueuedWriter(s, 2)
	for i := 0; i < 20; i++ {
		if err := w.Submit(context.Background(), Event{ID: "queue-" + itoa(i), RunID: "queue", Sequence: i + 1, Kind: "agent", Payload: []byte("safe")}); err != nil {
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
