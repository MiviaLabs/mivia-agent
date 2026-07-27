package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ---------------------------------------------------------------------------
// Tier 3 — Session + Agent Loop Persistence Integration
// ---------------------------------------------------------------------------
// These tests wire the chat Session with a real httptest-backed provider,
// real tools, and a FileSessionStore/SaveManager for persistence.

// sessionHTTPServer creates an httptest.Server with scripted LLM responses.
// Returns SSE-formatted responses for streaming agent loop requests.
func sessionHTTPServer(t *testing.T, steps []struct {
	content   string
	toolCalls []provider.ToolCall
}) *httptest.Server {
	t.Helper()
	var callCount int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := callCount
		callCount++

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = io.WriteString(w, s)
			if flusher != nil {
				flusher.Flush()
			}
		}

		if idx >= len(steps) {
			write("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
			write("data: [DONE]\n\n")
			return
		}
		step := steps[idx]
		if len(step.toolCalls) == 0 {
			// SSE delta for stop response
			write("data: {\"choices\":[{\"delta\":{\"content\":\"" + step.content + "\"},\"finish_reason\":\"stop\"}]}\n\n")
			write("data: [DONE]\n\n")
			return
		}
		// Tool calls response via SSE
		// First emit any content
		if step.content != "" {
			write("data: {\"choices\":[{\"delta\":{\"content\":\"" + step.content + "\"}}]}\n\n")
		}
		// Then emit tool calls deltas
		for i, tc := range step.toolCalls {
			write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":" + itoaSSE(i) + ",\"id\":\"" + tc.ID + "\",\"type\":\"function\",\"function\":{\"name\":\"" + tc.Function.Name + "\"}}]}}]}\n\n")
			write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":" + itoaSSE(i) + ",\"function\":{\"arguments\":\"" + jsonEscape(tc.Function.Arguments) + "\"}}]}}]}\n\n")
		}
		write("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		write("data: [DONE]\n\n")
	}))
}

func itoaSSE(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for n := i; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}

func jsonEscape(s string) string {
	// Escape for embedding in JSON string within SSE
	result := ""
	for _, ch := range s {
		switch ch {
		case '"':
			result += "\\\""
		case '\\':
			result += "\\\\"
		case '\n':
			result += "\\n"
		case '\t':
			result += "\\t"
		default:
			result += string(ch)
		}
	}
	return result
}

// newSessionIntegrationHelper creates a Session with real provider + tools.
func newSessionIntegrationHelper(t *testing.T, srv *httptest.Server) (*Session, *workspace.Root) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	comp := provider.NewOpenAICompat("session-test", srv.URL, "test-key", "", "")

	res := &config.Resolved{
		Model:        "session-model",
		SystemPrompt: "You are a helpful assistant with tools.",
	}
	s := NewSession(res, comp)
	s.Tools = reg
	s.UseTools = true
	s.MaxSteps = 5
	s.OnAgentEvent = func(e agent.Event) {
		// no-op capture for diagnostics
	}
	return s, ws
}

// TestSessionAgentLoopSaveAfterTurn verifies that after a SendUser turn
// with tool execution, the messages are persisted via the SaveManager.
func TestSessionAgentLoopSaveAfterTurn(t *testing.T) {
	srv := sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{{
		content: "I will create a file",
		toolCalls: []provider.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "write_file", Arguments: `{"path":"persist.txt","content":"persisted content"}`},
		}},
	}, {
		content: "File created successfully",
	}})
	defer srv.Close()

	s, ws := newSessionIntegrationHelper(t, srv)

	// Wire up persistence.
	sessionDir := filepath.Join(t.TempDir(), ".mivia", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSessionStore(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewSaveManager(store, "session-model", "session-test")
	s.SetSessionStore(store, mgr)

	// Run the agent loop via SendUser.
	ctx := context.Background()
	reply, err := s.SendUser(ctx, "create a file called persist.txt", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("agent reply: %q", reply)

	// Verify the file was actually written to disk via the tool.
	filePath := filepath.Join(ws.Abs, "persist.txt")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file not found at %s: %v", filePath, err)
	}
	if !strings.Contains(string(data), "persisted content") {
		t.Fatalf("file content=%q, expected 'persisted content'", string(data))
	}
	t.Logf("file verified at %s: %s", filePath, string(data))

	// Verify session messages count (should have system + user + assistant + tool results).
	s.mu.RLock()
	msgCount := len(s.Messages)
	s.mu.RUnlock()
	if msgCount < 3 {
		t.Fatalf("expected at least 3 messages (sys+user+assistant), got %d", msgCount)
	}

	// Verify auto-save created session files on disk.
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("expected at least one auto-saved session file on disk")
	}
	t.Logf("saved %d session files after agent turn", len(infos))
	for _, info := range infos {
		t.Logf("  session: %q (model=%s, messages=%d)", info.Name, info.Model, info.MessageCount)
	}
}

// TestSessionAgentLoopMultipleTurns verifies multiple SendUser calls
// with tool execution and persistence after each turn.
func multiTurnServer(t *testing.T) *httptest.Server {
	return sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		{content: "Creating first file", toolCalls: []provider.ToolCall{mkTC("call_t1", "write_file", `{"path":"turn1.txt","content":"first turn"}`)}},
		{content: "First file created"},
		{content: "Creating second file", toolCalls: []provider.ToolCall{mkTC("call_t2", "write_file", `{"path":"turn2.txt","content":"second turn"}`)}},
		{content: "Second file created"},
	})
}

func mkTC(id, name, args string) provider.ToolCall {
	return provider.ToolCall{
		ID: id, Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args},
	}
}

func verifyTurnFile(t *testing.T, ws *workspace.Root, name, expect string) {
	t.Helper()
	path := filepath.Join(ws.Abs, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not found at %s: %v", path, err)
	}
	if !strings.Contains(string(data), expect) {
		t.Fatalf("file %s content=%q, expected %q", name, string(data), expect)
	}
}

func TestSessionAgentLoopMultipleTurns(t *testing.T) {
	srv := multiTurnServer(t)
	defer srv.Close()

	s, ws := newSessionIntegrationHelper(t, srv)

	sessionDir := filepath.Join(t.TempDir(), ".mivia", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSessionStore(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewSaveManager(store, "session-model", "session-test")
	s.SetSessionStore(store, mgr)

	ctx := context.Background()
	reply1, err := s.SendUser(ctx, "create turn1.txt", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("turn 1 reply: %q", reply1)
	verifyTurnFile(t, ws, "turn1.txt", "first turn")

	reply2, err := s.SendUser(ctx, "create turn2.txt", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("turn 2 reply: %q", reply2)
	verifyTurnFile(t, ws, "turn2.txt", "second turn")

	s.mu.RLock()
	msgCount := len(s.Messages)
	s.mu.RUnlock()
	t.Logf("message count after 2 turns: %d", msgCount)

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("saved %d session files after 2 turns", len(infos))
}

// TestSessionSaveManagerWithRealLoop verifies SaveAfterTurn creates
// properly-named auto-save sessions with correct model/provider metadata.
func TestSessionSaveManagerWithRealLoop(t *testing.T) {
	srv := sessionHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{{
		content: "I will check files",
		toolCalls: []provider.ToolCall{{
			ID:   "call_glob",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "glob", Arguments: `{"pattern":"*.txt"}`},
		}},
	}, {
		content: "No txt files found",
	}})
	defer srv.Close()

	s, _ := newSessionIntegrationHelper(t, srv)

	sessionDir := filepath.Join(t.TempDir(), ".mivia", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileSessionStore(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewSaveManager(store, "session-model", "session-test")
	s.SetSessionStore(store, mgr)

	ctx := context.Background()
	_, err = s.SendUser(ctx, "list all txt files", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// List sessions to verify auto-save created files.
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 {
		t.Fatal("expected auto-save sessions on disk")
	}

	// Verify metadata is preserved.
	info := infos[0]
	if info.Model != "session-model" {
		t.Fatalf("model=%q, want session-model", info.Model)
	}
	if info.Provider != "session-test" {
		t.Fatalf("provider=%q, want session-test", info.Provider)
	}
	if info.MessageCount < 3 {
		t.Fatalf("message_count=%d, want >=3", info.MessageCount)
	}
	t.Logf("session info: name=%q model=%q provider=%q messages=%d",
		info.Name, info.Model, info.Provider, info.MessageCount)

	// Load the session and verify messages are intact.
	s2, _ := newSessionIntegrationHelper(t, srv)
	s2.SetSessionStore(store, nil)
	if err := s2.Load(info.Name); err != nil {
		t.Fatal(err)
	}
	if len(s2.Messages) != info.MessageCount {
		t.Fatalf("loaded message count=%d, expected %d", len(s2.Messages), info.MessageCount)
	}
	t.Logf("loaded %d messages from session %q", len(s2.Messages), info.Name)
}
