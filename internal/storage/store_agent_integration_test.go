package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ---------------------------------------------------------------------------
// Tier 4 - Storage + Agent Events Integration
// ---------------------------------------------------------------------------
// These tests wire agent events through EventBus into the storage layer
// (SQLite + QueuedWriter).

// storageEventHandler subscribes to EventBus and writes events to storage.
//
// One delivery goroutine runs per subscription, so a handler registered for
// several kinds is called concurrently: the sequence counter has to be atomic.
type storageEventHandler struct {
	store *QueuedWriter
	seq   atomic.Int64
}

func (h *storageEventHandler) HandleEvent(ctx context.Context, ev events.Event) {
	seq := int(h.seq.Add(1))
	payload, _ := json.Marshal(map[string]string{
		"detail": ev.Detail,
		"input":  ev.Input,
		"output": ev.Output,
		"name":   ev.Name,
	})
	_ = h.store.Submit(ctx, Event{
		ID:       fmt.Sprintf("%s-%d", ev.ToolCallID, seq),
		RunID:    "agent-run",
		Sequence: seq,
		Kind:     string(ev.Kind),
		Payload:  payload,
	})
}

// sseWrite writes an SSE data line to the response.
func sseWrite(w http.ResponseWriter, flusher http.Flusher, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

// storageEventTestServer creates a non-streaming httptest server that
// returns a tool_calls response on first call and stop on subsequent calls.
// Events from the agent loop are published to the provided EventBus.
func storageEventTestServer(t *testing.T, dir string, bus *events.Bus) (*tools.Registry, []Event) {
	t.Helper()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		idx := int(callCount.Load())
		callCount.Add(1)
		if idx == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []map[string]any{{
							"id": "call_read", "type": "function",
							"function": map[string]string{
								"name": "read_file", "arguments": `{"path":"test.txt"}`,
							},
						}},
					},
				}},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]string{"role": "assistant", "content": "File read and content retrieved"},
				}},
			})
		}
	}))
	t.Cleanup(srv.Close)

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{Name: "storage-test", BaseURL: srv.URL, APIKey: "test-key"})
	loop := &agent.Loop{Completer: comp, Tools: reg}

	if _, err := reg.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"test.txt","content":"hello storage"}`)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := loop.Run(ctx, "read test.txt", agent.Options{
		Model: "storage-model", MaxSteps: 5, MaxConcurrentTools: 2,
		ToolTimeout: 5 * time.Second, EventBus: bus,
	}); err != nil {
		t.Fatal(err)
	}

	// Flush the bus so all async events reach the handler before we return.
	bus.Flush()

	return reg, nil
}

// TestAgentEventsPersistToSQLite verifies agent loop events flow through
// EventBus into SQLite storage via QueuedWriter.
func TestAgentEventsPersistToSQLite(t *testing.T) {
	dir := t.TempDir()
	qw := NewQueuedWriter(&flushSQLite{dir: dir}, 10)

	bus := events.New()
	handler := &storageEventHandler{store: qw}
	bus.SubscribeMany([]events.Kind{
		events.KindToolStart, events.KindToolEnd,
		events.KindAssistant, events.KindStep, events.KindToolParallel,
	}, handler)

	storageEventTestServer(t, dir, bus)
	// Delivery is async: close the bus so every queued event has reached the
	// handler before the writer stops accepting submissions.
	bus.Close()
	if err := qw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open a fresh read-only connection to verify persisted events.
	s, err := OpenSQLite(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evts, err := s.Events(context.Background(), "agent-run")
	if err != nil {
		t.Fatal(err)
	}

	if len(evts) == 0 {
		t.Fatal("expected at least 1 persisted event")
	}
	t.Logf("persisted %d events to SQLite", len(evts))
	hasToolStart := eventKindPresent(evts, string(events.KindToolStart))
	hasToolEnd := eventKindPresent(evts, string(events.KindToolEnd))
	if !hasToolStart {
		t.Fatal("expected ToolStart event")
	}
	if !hasToolEnd {
		t.Fatal("expected ToolEnd event")
	}
}

func eventKindPresent(evts []Event, kind string) bool {
	for _, e := range evts {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// flushSQLite opens SQLite lazily so QueuedWriter can close it after submission.
type flushSQLite struct {
	dir   string
	store *SQLite
	once  sync.Once
	err   error
}

func (f *flushSQLite) Append(ctx context.Context, e Event) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.Append(ctx, e)
}

func (f *flushSQLite) AppendClaimed(ctx context.Context, e Event, holder string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.AppendClaimed(ctx, e, holder)
}

func (f *flushSQLite) AppendWithExistingClaim(ctx context.Context, e Event, holder string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.AppendWithExistingClaim(ctx, e, holder)
}

func (f *flushSQLite) Events(ctx context.Context, runID string) ([]Event, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return nil, f.err
	}
	return f.store.Events(ctx, runID)
}

func (f *flushSQLite) EventsSince(ctx context.Context, runID string, afterSequence int) ([]Event, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return nil, f.err
	}
	return f.store.EventsSince(ctx, runID, afterSequence)
}

func (f *flushSQLite) DeleteRun(ctx context.Context, runID string, throughSequence int) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.DeleteRun(ctx, runID, throughSequence)
}

func (f *flushSQLite) AppendAndDeleteRun(ctx context.Context, tombstone Event, claim Claim) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.AppendAndDeleteRun(ctx, tombstone, claim)
}

func (f *flushSQLite) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return nil, afterCursor, f.err
	}
	return f.store.Changes(ctx, afterCursor)
}

func (f *flushSQLite) Count(ctx context.Context) (int, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return 0, f.err
	}
	return f.store.Count(ctx)
}

func (f *flushSQLite) ListRunIDs(ctx context.Context) ([]string, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return nil, f.err
	}
	return f.store.ListRunIDs(ctx)
}

func (f *flushSQLite) Close() error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.Close()
}

func (f *flushSQLite) ClaimRun(ctx context.Context, runID, holder string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.ClaimRun(ctx, runID, holder)
}

func (f *flushSQLite) TakeoverClaim(ctx context.Context, runID, holder string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.TakeoverClaim(ctx, runID, holder)
}

func (f *flushSQLite) ReleaseClaim(ctx context.Context, runID, holder string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.ReleaseClaim(ctx, runID, holder)
}

func (f *flushSQLite) ClearClaim(ctx context.Context, runID string) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.ClearClaim(ctx, runID)
}

func (f *flushSQLite) PutContent(ctx context.Context, ref string, data []byte) error {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return f.err
	}
	return f.store.PutContent(ctx, ref, data)
}

func (f *flushSQLite) GetContent(ctx context.Context, ref string) ([]byte, error) {
	f.once.Do(func() { f.store, f.err = OpenSQLite(filepath.Join(f.dir, "events.db")) })
	if f.err != nil {
		return nil, f.err
	}
	return f.store.GetContent(ctx, ref)
}

// TestAgentEventQueueBackpressure verifies the QueuedWriter's bounded
// capacity with agent events - events aren't dropped but queued.
func TestAgentEventQueueBackpressure(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Use a small queue capacity to trigger backpressure.
	qw := NewQueuedWriter(s, 3)

	// Submit many events rapidly - they should all eventually commit.
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if err := qw.Submit(ctx, Event{
			ID:       fmt.Sprintf("ev-%d", i),
			RunID:    "backpressure-test",
			Sequence: i + 1,
			Kind:     "tool",
			Payload:  []byte(`{"type":"test"}`),
		}); err != nil {
			t.Fatalf("event %d rejected: %v", i, err)
		}
	}

	if err := qw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open a fresh connection to read persisted events.
	s2, err := OpenSQLite(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// All events should be committed.
	m := qw.Metrics()
	if m.Submitted != 20 {
		t.Fatalf("submitted=%d, want 20", m.Submitted)
	}
	if m.Committed != 20 {
		t.Fatalf("committed=%d, want 20", m.Committed)
	}
	if m.Rejected != 0 {
		t.Fatalf("rejected=%d, want 0", m.Rejected)
	}

	count, err := s2.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 20 {
		t.Fatalf("SQLite count=%d, want 20", count)
	}
}

// var deadcode guard: ensure these strings are used
var _ = strings.Contains
