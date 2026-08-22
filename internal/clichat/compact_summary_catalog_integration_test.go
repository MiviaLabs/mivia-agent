package clichat

// Integration test for `mivia compact --session`: with [context.summary]
// default-on, a catalog compaction must perform a REAL LLM summary against
// the configured provider and durably inject the host-rendered summary into
// the checkpoint's active context - not stay structural-only.
//
// The defect this RED test pins: runCompactWithIO builds its session through
// newCatalogSessionAt, which does chat.NewSession(res, nil). That nil
// completer lands in the session binding, so summaryWiring
// (internal/cli/context_summary_setup.go) bails on
// binding.Completer==nil, the ContextManager never gets a Summarizer, and
// `mivia compact --session` compacts structure but never calls the LLM. The
// assertions below (a summary request reached the provider; the durable
// active_context carries the "[host-injected context summary" message) both
// fail on current code.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// catalogSummarySystemMarker mirrors summarySystemMarker in
// context_summary_integration_test.go: the first sentence of the summarize
// system prompt, used to classify requests on the wire.
const catalogSummarySystemMarker = "You summarize an earlier part of a conversation."

// catalogSummaryEchoReply mirrors summaryEchoReply: a valid summary reply
// that echoes the version and source_range values out of the prompt's
// "Echo these values" block.
func catalogSummaryEchoReply(req provider.Request) string {
	version := "1"
	sourceRange := "{}"
	for _, line := range strings.Split(req.Messages[len(req.Messages)-1].Content, "\n") {
		if v, ok := strings.CutPrefix(line, "version: "); ok {
			version = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "source_range: "); ok {
			sourceRange = strings.TrimSpace(v)
		}
	}
	return fmt.Sprintf(`{"version":%s,"objective":"the user objective","state":"work continued","decisions":[],"evidence":[],"changed_surfaces":[],"open_work":[],"risks":[],"source_range":%s}`, version, sourceRange)
}

// catalogIsSummaryRequest reports whether a request's first message is the
// summarize system prompt.
func catalogIsSummaryRequest(req provider.Request) bool {
	return len(req.Messages) > 0 && req.Messages[0].Role == provider.RoleSystem &&
		strings.HasPrefix(req.Messages[0].Content, catalogSummarySystemMarker)
}

// catalogSummaryStub is an OpenAI-compatible httptest handler that records
// every chat-completions request it receives, answers summary requests with
// the envelope-echo JSON, and every other request with "ok".
type catalogSummaryStub struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (s *catalogSummaryStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var payload struct {
		Messages []provider.Message `json:"messages"`
	}
	// Recording must not fail the stub if the body has unexpected fields.
	_ = json.Unmarshal(body, &payload)
	req := provider.Request{Messages: payload.Messages}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()

	content := "ok"
	if catalogIsSummaryRequest(req) {
		content = catalogSummaryEchoReply(req)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"role": "assistant", "content": content},
		}},
	})
}

// summaryRequests returns a copy of every recorded summary request.
func (s *catalogSummaryStub) summaryRequests() []provider.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provider.Request
	for _, req := range s.requests {
		if catalogIsSummaryRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

// catalogCompactWorkspace builds a HOME-isolated temp workspace whose
// mivia.toml points the ollama provider at the OpenAI-compatible stub and
// pins the durable context store (via [subagents] store_path) under the
// workspace, so neither config discovery nor storage ever touches the real
// ~/.mivia. [context.summary] stays default-on (no section needed).
func catalogCompactWorkspace(t *testing.T, server *httptest.Server) (ws, storePath, cfgPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OLLAMA_API_KEY", "")
	ws = t.TempDir()
	cfgPath = filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	storePath = filepath.Join(ws, ".mivia", "context.db")
	fixture := fmt.Sprintf(`[provider]
name = "ollama"

[providers.ollama]
base_url = "%s/v1"
api_key_env = "OLLAMA_API_KEY"
models = [{ name = "llama3.1:8b", context_window_tokens = 128000 }]

[subagents]
store_path = %q
`, server.URL, storePath)
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIVIA_CONFIG", cfgPath)
	return ws, storePath, cfgPath
}

// durableActiveContext opens the pinned context store and returns the durable
// checkpoint's active messages for the session, via ReclaimSession - the
// documented cross-process resume path (the minted capability of the process
// that wrote the row is unrecoverable, so a later reader reclaims ownership).
func durableActiveContext(t *testing.T, ws, storePath, sessionID string) []provider.Message {
	t.Helper()
	store, err := storage.OpenSQLite(storePath)
	if err != nil {
		t.Fatalf("open context store %s: %v", storePath, err)
	}
	defer store.Close()
	root, err := chatWorkspaceRoot(ws)
	if err != nil {
		t.Fatalf("chatWorkspaceRoot(%s): %v", ws, err)
	}
	principal, err := contextstate.NewPrincipal(contextWorkspaceID(root), sessionID, "local-user")
	if err != nil {
		t.Fatalf("principal for %s: %v", sessionID, err)
	}
	snapshot, err := store.ReclaimSession(context.Background(), principal, sessionID)
	if err != nil {
		t.Fatalf("reclaim session %s: %v", sessionID, err)
	}
	var messages []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &messages); err != nil {
		t.Fatalf("decode active_context: %v", err)
	}
	return messages
}

// TestCatalogCompactSummarizes drives the REAL `mivia compact --session`
// entrypoint (runCompactWithIO) against a temp workspace with REAL SQLite
// storage and a real OpenAI-compatible httptest provider: (1) HOME is pinned
// so the context store never touches the real ~/.mivia; (2) a compactable
// catalog session is seeded with real committed turns
// (seedCompactableCatalogSession: chat.NewSession + setupChatSessionContext +
// repeated SendUser); (3) runCompactWithIO(["--session", id, "--workspace",
// ws, "--json"]) runs; (4) the stub must have received a summary request
// (first message is the summarize system prompt) and the durable
// context_checkpoints.active_context must contain a user message whose
// content carries "[host-injected context summary".
func TestCatalogCompactSummarizes(t *testing.T) {
	stub := &catalogSummaryStub{}
	server := httptest.NewServer(stub)
	t.Cleanup(server.Close)

	ws, storePath, _ := catalogCompactWorkspace(t, server)
	sessionID := seedCompactableCatalogSession(t, ws)

	var buf bytes.Buffer
	if err := runCompactWithIO([]string{"--session", sessionID, "--workspace", ws, "--json"}, &buf); err != nil {
		t.Fatalf("compact %s: %v", sessionID, err)
	}

	summaryReqs := stub.summaryRequests()
	if len(summaryReqs) == 0 {
		t.Fatal("mivia compact --session sent no summary request: compaction stayed structural-only (newCatalogSessionAt's nil completer bails summaryWiring on binding.Completer==nil)")
	}
	first := summaryReqs[0].Messages
	if len(first) == 0 || first[0].Role != provider.RoleSystem || !strings.HasPrefix(first[0].Content, catalogSummarySystemMarker) {
		t.Fatalf("summary request's first message is not the summarize system prompt: %+v", first)
	}

	messages := durableActiveContext(t, ws, storePath, sessionID)
	for _, m := range messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "[host-injected context summary") {
			return
		}
	}
	t.Fatalf("durable context_checkpoints.active_context (%d messages) carries no user message containing %q", len(messages), "[host-injected context summary")
}
