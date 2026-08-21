package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// --- stub provider -------------------------------------------------------
//
// Same OpenAI-compatible SSE wire shape as internal/cli/characterization_test.go's
// stubServer/sseToolCallTurn/sseTextTurn (that file is off limits to this
// slice; this is a parallel, package-local copy of the same recipe, not a
// shared helper - internal/composition must not import internal/cli).

type sessionStubServer struct {
	turns []string
	calls int
}

func newSessionStubServer(turns ...string) *sessionStubServer {
	return &sessionStubServer{turns: turns}
}

func (s *sessionStubServer) handler(w http.ResponseWriter, _ *http.Request) {
	idx := s.calls
	if idx >= len(s.turns) {
		idx = len(s.turns) - 1
	}
	s.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, s.turns[idx])
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *sessionStubServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sseToolCallTurn(id, name, argumentsJSON string) string {
	argsEscaped, _ := json.Marshal(argumentsJSON)
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n",
		id, name, string(argsEscaped))
}

func sseTextTurn(content string) string {
	body, _ := json.Marshal(content)
	return fmt.Sprintf(
		"data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n",
		string(body))
}

// --- test -----------------------------------------------------------------

// recordingEventSink collects the Kind of every event published on a bus, in
// publish order, guarded by a mutex since Bus delivers on its own goroutine.
type recordingEventSink struct {
	mu    sync.Mutex
	kinds []events.Kind
}

func (r *recordingEventSink) HandleEvent(_ context.Context, ev events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds = append(r.kinds, ev.Kind)
}

func (r *recordingEventSink) snapshot() []events.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Kind, len(r.kinds))
	copy(out, r.kinds)
	return out
}

// buildSessionInputConfig mirrors internal/cli/characterization_test.go's
// baseResolvedConfig: the minimal config.Resolved provider.New needs to
// build a real completer against a stub server, plus a run_command
// allowlist entry so the turn below has a real tool to call.
func buildSessionInputConfig(stubURL string) *config.Resolved {
	return &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      stubURL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
	}
}

// TestBuildSession_EndToEndTurn drives BuildSession's output through one
// full turn: a tool call followed by a final assistant message, against a
// stub OpenAI-wire provider. It asserts the session completes, the event
// bus sees the kinds the agent loop's emit() dual-publishes for this shape
// (see internal/agent/emit.go), and a checkpoint row lands in the SQLite
// store BuildSession opened.
func TestBuildSession_EndToEndTurn(t *testing.T) {
	stub := newSessionStubServer(
		sseToolCallTurn("call_1", "run_command", `{"argv":["echo","ok"]}`),
		sseTextTurn("done"),
	)
	res := buildSessionInputConfig(stub.start(t))
	comp, err := provider.New(res)
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	storePath := filepath.Join(t.TempDir(), "context.db")

	sess, store, principal, err := BuildSession(SessionInput{
		Config:      res,
		Completer:   comp,
		Registry:    RegistryInput{Workspace: ws, RunAllowlist: []string{"echo"}},
		StorePath:   storePath,
		WorkspaceID: "test-workspace",
	})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	defer store.Close()

	sink := &recordingEventSink{}
	sess.EventBus.SubscribeMany([]events.Kind{events.KindAssistant, events.KindToolStart, events.KindToolEnd}, sink)

	reply, err := sess.SendUser(context.Background(), "run echo ok", io.Discard)
	if err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if reply != "done" {
		t.Fatalf("reply = %q, want %q", reply, "done")
	}

	sess.EventBus.Flush()
	kinds := sink.snapshot()
	if len(kinds) == 0 {
		t.Fatal("no events observed on the bus for a tool-call turn")
	}
	if kinds[len(kinds)-1] != events.KindAssistant {
		t.Fatalf("last event kind = %s, want %s (final assistant message)", kinds[len(kinds)-1], events.KindAssistant)
	}
	sawToolStart, sawToolEnd := false, false
	for _, k := range kinds {
		switch k {
		case events.KindToolStart:
			sawToolStart = true
		case events.KindToolEnd:
			sawToolEnd = true
		}
	}
	if !sawToolStart {
		t.Fatalf("no %s event observed: %v", events.KindToolStart, kinds)
	}
	if !sawToolEnd {
		t.Fatalf("no %s event observed: %v", events.KindToolEnd, kinds)
	}
	t.Logf("observed event kind sequence: %v", kinds)

	assertCheckpointRowExists(t, store, principal, sess.SessionID)
}

// assertCheckpointRowExists loads the session's checkpoint through
// storage.SQLite's own Load, the same call the CLI's resume/reclaim paths
// use to read a persisted checkpoint back. A non-empty Active record proves
// PreparationCommitter (buildSessionCheckpointStore's CheckpointPublisher)
// actually wrote a context_checkpoints row for this turn, not just a bare
// context_sessions head.
//
// principal must be the exact value BuildSession installed
// (contextstate.Principal's capability is random per mint, and store.Load
// rejects a principal whose capability does not match what was written) -
// see BuildSession's returned principal.
func assertCheckpointRowExists(t *testing.T, store *storage.SQLite, principal contextstate.Principal, sessionID string) {
	t.Helper()
	snapshot, err := store.Load(context.Background(), principal, sessionID)
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(snapshot.Active.ActiveContext) == 0 {
		t.Fatalf("no active checkpoint content for session %q: %+v", sessionID, snapshot)
	}
}
