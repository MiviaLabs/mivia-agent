package chat

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestSaveAndLoadRoundTrip tests the basic save/load cycle.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	s := newTestSession(t, "test-model")
	_, err := s.SendUser(context.Background(), "hello", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SendUser(context.Background(), "what is the weather?", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	savedMsgCount := len(s.Messages)
	savedTurnCount := s.UserTurns()

	if err := s.Save("test-roundtrip"); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Create a fresh session and load.
	s2 := newTestSession(t, "different-model") // model should be overwritten on load
	s2.SessionDir = s.SessionDir

	if err := s2.Load("test-roundtrip"); err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(s2.Messages) != savedMsgCount {
		t.Fatalf("message count: got %d, want %d", len(s2.Messages), savedMsgCount)
	}
	if s2.UserTurns() != savedTurnCount {
		t.Fatalf("turn count: got %d, want %d", s2.UserTurns(), savedTurnCount)
	}
	if s2.Model != "test-model" {
		t.Fatalf("model: got %q, want %q", s2.Model, "test-model")
	}

	// Verify content of first user message.
	userMsg := findFirstUser(s2.Messages)
	if userMsg != "hello" {
		t.Fatalf("first user msg: got %q, want %q", userMsg, "hello")
	}
}

// TestSaveAndLoadSystemPrompt verifies the system prompt is preserved.
func TestSaveAndLoadSystemPrompt(t *testing.T) {
	s := newTestSession(t, "m")
	// System prompt should be in messages[0].
	if len(s.Messages) == 0 || s.Messages[0].Role != provider.RoleSystem {
		t.Fatal("expected system message")
	}
	sysContent := s.Messages[0].Content

	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Save("test-sys"); err != nil {
		t.Fatal(err)
	}

	s2 := newTestSession(t, "m2")
	s2.SessionDir = s.SessionDir
	if err := s2.Load("test-sys"); err != nil {
		t.Fatal(err)
	}

	if len(s2.Messages) == 0 || s2.Messages[0].Role != provider.RoleSystem {
		t.Fatal("loaded session missing system message")
	}
	if s2.Messages[0].Content != sysContent {
		t.Fatalf("system content: got %q, want %q", s2.Messages[0].Content, sysContent)
	}
}

// TestChunking verifies that sessions with > ChunkMessageThreshold messages
// are split into multiple chunk files.
func TestChunking(t *testing.T) {
	s := newTestSession(t, "chunk-model")
	// Disable system prompt to simplify counting.
	s.Messages = nil

	// Add just over one chunk's worth of messages.
	target := ChunkMessageThreshold + 50
	for i := 0; i < target; i++ {
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleUser,
			Content: "msg",
		})
		s.Messages = append(s.Messages, provider.Message{
			Role:    provider.RoleAssistant,
			Content: "ok",
		})
	}

	if err := s.Save("test-chunked"); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify multiple chunk files exist.
	dir := filepath.Join(s.SessionDir, "test-chunked")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	chunkFiles := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chunk_") && strings.HasSuffix(e.Name(), ".jsonl") {
			chunkFiles++
		}
	}
	if chunkFiles < 2 {
		t.Fatalf("expected at least 2 chunk files, got %d", chunkFiles)
	}

	// Load and verify all messages are preserved.
	s2 := newTestSession(t, "chunk-model2")
	s2.SessionDir = s.SessionDir
	if err := s2.Load("test-chunked"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(s2.Messages) != target*2 {
		t.Fatalf("message count: got %d, want %d", len(s2.Messages), target*2)
	}
}

// TestListSessions verifies listing saved sessions.
func TestListSessions(t *testing.T) {
	s := newTestSession(t, "m1")
	_, err := s.SendUser(context.Background(), "first", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("session-one"); err != nil {
		t.Fatal(err)
	}

	// Use a deterministic way to ensure ordering: write second, then first in reverse.
	// Just save them sequentially; filesystem timestamps ensure ordering.
	s2 := newTestSession(t, "m2")
	s2.SessionDir = s.SessionDir
	_, err = s2.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Save("session-two"); err != nil {
		t.Fatal(err)
	}

	// List from the first session (same dir).
	infos, err := s.ListSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// We expect the named sessions plus any per-turn auto-saves from SendUser.
	// Named sessions must appear and be correctly ordered.
	if len(infos) < 2 {
		t.Fatalf("expected at least 2 sessions (2 named), got %d", len(infos))
	}
	// Named sessions should be at the top (most recent) in order.
	// Auto-saves (__last__*) may also appear.
	namedCount := 0
	for _, si := range infos {
		if si.Name == "session-two" {
			namedCount++
		}
		if si.Name == "session-one" {
			namedCount++
		}
	}
	if namedCount < 2 {
		t.Fatalf("expected both named sessions, found %d", namedCount)
	}
	// Named sessions should be sorted most-recent first.
	// Find session-two and session-one positions.
	posTwo, posOne := -1, -1
	for i, si := range infos {
		if si.Name == "session-two" {
			posTwo = i
		}
		if si.Name == "session-one" {
			posOne = i
		}
	}
	if posTwo >= posOne {
		t.Fatalf("sort order: session-two should come before session-one, got positions %d, %d", posTwo, posOne)
	}
	// Check metadata on session-one.
	for _, si := range infos {
		if si.Name == "session-one" {
			if si.TurnCount != 1 || si.MessageCount != 3 { // sys + user + assistant
				t.Fatalf("session-one: turns=%d msgs=%d", si.TurnCount, si.MessageCount)
			}
		}
	}
}

// TestDeleteSession verifies deletion.
func TestDeleteSession(t *testing.T) {
	s := newTestSession(t, "m")
	if err := s.Save("test-delete-me"); err != nil {
		t.Fatal(err)
	}

	// Verify it exists.
	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 session before delete, got %d", len(infos))
	}

	if err := s.DeleteSession("test-delete-me"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	infos, err = s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(infos))
	}
}

// TestDeleteNonExistentSession verifies error for missing session.
func TestDeleteNonExistentSession(t *testing.T) {
	s := newTestSession(t, "m")
	err := s.DeleteSession("does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

// TestLoadNonExistentSession verifies error for missing session.
func TestLoadNonExistentSession(t *testing.T) {
	s := newTestSession(t, "m")
	err := s.Load("does-not-exist")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

// TestAutoSave verifies SaveLast creates a timestamped auto-save session.
func TestAutoSave(t *testing.T) {
	s := newTestSession(t, "m")
	_, err := s.SendUser(context.Background(), "auto save test", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SaveLast(); err != nil {
		t.Fatalf("SaveLast: %v", err)
	}

	if !s.HasAutoSave() {
		t.Fatal("expected auto-save to exist")
	}

	latest := s.LatestAutoSaveName()
	if latest == "" {
		t.Fatal("expected a latest auto-save name")
	}
	if !strings.HasPrefix(latest, AutoSaveName) {
		t.Fatalf("expected name to start with %q, got %q", AutoSaveName, latest)
	}

	// Load auto-save via LatestAutoSaveName.
	s2 := newTestSession(t, "m-loaded")
	s2.SessionDir = s.SessionDir
	if err := s2.Load(latest); err != nil {
		t.Fatalf("load auto-save %q: %v", latest, err)
	}
	if s2.UserTurns() != 1 {
		t.Fatalf("expected 1 turn, got %d", s2.UserTurns())
	}
	if findFirstUser(s2.Messages) != "auto save test" {
		t.Fatalf("unexpected content: %q", findFirstUser(s2.Messages))
	}
}

// TestAutoSaveEmptySession verifies SaveLast is a no-op for empty sessions.
func TestAutoSaveEmptySession(t *testing.T) {
	s := newTestSession(t, "m")
	// No user turns, just system prompt.
	if err := s.SaveLast(); err != nil {
		t.Fatalf("SaveLast on empty: %v", err)
	}
	if s.HasAutoSave() {
		t.Fatal("should not auto-save empty session")
	}
}

// TestAutoSaveNoDir verifies SaveLast skips when no SessionDir set.
func TestAutoSaveNoDir(t *testing.T) {
	s := newTestSession(t, "m")
	s.SessionDir = "" // not set
	// Should not panic or error.
	if err := s.SaveLast(); err != nil {
		t.Fatalf("SaveLast without dir: %v", err)
	}
}

// TestAutoSaveTimestampNames verifies each SaveLast creates a unique timestamped name.
func TestAutoSaveTimestampNames(t *testing.T) {
	s := newTestSession(t, "m")
	_, err := s.SendUser(context.Background(), "first", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLast(); err != nil {
		t.Fatalf("first SaveLast: %v", err)
	}
	name1 := s.LatestAutoSaveName()

	_, err = s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLast(); err != nil {
		t.Fatalf("second SaveLast: %v", err)
	}
	name2 := s.LatestAutoSaveName()

	if name1 == name2 {
		t.Fatalf("expected different auto-save names, got same %q", name1)
	}

	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	autoCount := 0
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			autoCount++
		}
	}
	if autoCount < 2 {
		t.Fatalf("expected at least 2 auto-saves, got %d", autoCount)
	}
}

// TestRollingAutoSave verifies that only the N most recent auto-saves are kept.
func TestRollingAutoSave(t *testing.T) {
	s := newTestSession(t, "m")
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
	}

	// Save AutoSaveKeep+3 times.
	saveCount := AutoSaveKeep + 3
	for i := 0; i < saveCount; i++ {
		s.Messages = append(s.Messages,
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("msg %d", i)},
			provider.Message{Role: provider.RoleAssistant, Content: "ok"},
		)
		if err := s.SaveLast(); err != nil {
			t.Fatalf("SaveLast iteration %d: %v", i, err)
		}
	}

	// Count auto-save sessions on disk.
	infos, err := s.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	autoInfos := make([]SessionInfo, 0)
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			autoInfos = append(autoInfos, si)
		}
	}
	if len(autoInfos) > AutoSaveKeep {
		t.Fatalf("expected at most %d auto-saves on disk, got %d", AutoSaveKeep, len(autoInfos))
	}
	if len(autoInfos) < AutoSaveKeep {
		t.Fatalf("expected exactly %d auto-saves (minimum kept), got %d; only %d saves attempted", AutoSaveKeep, len(autoInfos), saveCount)
	}

	// Verify the newest saves are the ones kept (sorted by UpdatedAt descending already).
	// The last saved should be most recent.
	if len(autoInfos) > 0 {
		latest := s.LatestAutoSaveName()
		if latest != autoInfos[0].Name {
			t.Fatalf("LatestAutoSaveName %q does not match most recent in list %q", latest, autoInfos[0].Name)
		}
	}
}

// TestHasAutoSaveLegacyDir verifies the old bare __last__ directory is still detected.
func TestHasAutoSaveLegacyDir(t *testing.T) {
	s := newTestSession(t, "m")
	_, err := s.SendUser(context.Background(), "legacy test", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// Save with the old bare name (simulating migration scenario).
	if err := s.Save(AutoSaveName); err != nil {
		t.Fatalf("Save bare __last__: %v", err)
	}

	if !s.HasAutoSave() {
		t.Fatal("HasAutoSave should detect bare __last__ directory")
	}

	// LatestAutoSaveName should also find it.
	latest := s.LatestAutoSaveName()
	if latest != AutoSaveName {
		t.Fatalf("expected LatestAutoSaveName %q, got %q", AutoSaveName, latest)
	}
}

// TestLatestAutoSaveName verifies the most recent auto-save is returned.
func TestLatestAutoSaveName(t *testing.T) {
	s := newTestSession(t, "m")
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
	}

	// No auto-saves yet.
	if latest := s.LatestAutoSaveName(); latest != "" {
		t.Fatalf("expected empty latest before any saves, got %q", latest)
	}

	// Save first.
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "first"})
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	if err := s.SaveLast(); err != nil {
		t.Fatal(err)
	}
	first := s.LatestAutoSaveName()
	if first == "" {
		t.Fatal("expected non-empty latest after first save")
	}

	// Save second.
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: "second"})
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	if err := s.SaveLast(); err != nil {
		t.Fatal(err)
	}
	second := s.LatestAutoSaveName()

	if first == second {
		t.Fatalf("expected different names for distinct saves: %q vs %q", first, second)
	}
	if !strings.HasPrefix(second, AutoSaveName) {
		t.Fatalf("expected second to start with %q, got %q", AutoSaveName, second)
	}
}

// TestIsAutoSaveName verifies auto-save name detection. The prefix alone is
// not sufficient — see TestIsAutoSaveNameRejectsUserNames for why a
// user-typed "__last__..." name must not be treated as an auto-save.
func TestIsAutoSaveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{AutoSaveName, true},
		{AutoSaveName + "20250115T103000", true},
		{AutoSaveName + "_foo", false},
		{"my-session", false},
		{"project-work", false},
		{"__last__", true},
		{"__last_", false},
		{"_last__", false},
		{"", false},
	}
	for _, tt := range tests {
		got := IsAutoSaveName(tt.name)
		if got != tt.want {
			t.Errorf("IsAutoSaveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestSanitizeSessionName verifies path traversal prevention.
func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-session", "my-session"},
		{"../evil", "__evil"},
		{"foo/bar", "foo_bar"},
		{"foo\\bar", "foo_bar"},
		{"a:b", "a_b"},
		{"", "unnamed"},
		{".", "unnamed"},
		{"..", "unnamed"},
		{"  spaced  ", "spaced"},
		{"\x00null", "null"},
	}
	for _, tt := range tests {
		got := sanitizeSessionName(tt.input)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestSaveOverwrite verifies re-saving updates timestamps but preserves created_at.
func TestSaveOverwrite(t *testing.T) {
	s := newTestSession(t, "m")
	_, err := s.SendUser(context.Background(), "first", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("test-overwrite"); err != nil {
		t.Fatal(err)
	}

	// Read meta to get created_at. Use manual write for ordering.
	dir := filepath.Join(s.SessionDir, "test-overwrite")
	meta1, err := readMetaJSON(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Add more messages, re-save.
	_, err = s.SendUser(context.Background(), "second", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("test-overwrite"); err != nil {
		t.Fatal(err)
	}

	meta2, err := readMetaJSON(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !meta2.CreatedAt.Equal(meta1.CreatedAt) {
		t.Fatal("created_at should be preserved on re-save")
	}
	if meta2.TurnCount != 2 {
		t.Fatalf("turn count: got %d, want 2", meta2.TurnCount)
	}
	if meta2.MessageCount != 5 { // sys + 2*(user+assistant)
		t.Fatalf("message count: got %d, want 5", meta2.MessageCount)
	}
}

// TestListSessionsIgnoresCorrupt verifies corrupt sessions are skipped gracefully.
func TestListSessionsIgnoresCorrupt(t *testing.T) {
	s := newTestSession(t, "m")
	if err := s.Save("good-session"); err != nil {
		t.Fatal(err)
	}

	// Create a directory that looks like a session but has no meta.json.
	badDir := filepath.Join(s.SessionDir, "corrupt-dir")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a chunk file but no meta.json
	if err := writeJSONL(filepath.Join(badDir, "chunk_0000.jsonl"), []provider.Message{
		{Role: provider.RoleUser, Content: "orphaned"},
	}); err != nil {
		t.Fatal(err)
	}

	infos, err := s.ListSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 session (corrupt skipped), got %d", len(infos))
	}
	if infos[0].Name != "good-session" {
		t.Fatalf("expected good-session, got %s", infos[0].Name)
	}
}

// TestJSONLwithToolCalls verifies that tool calls survive serialization.
func TestJSONLwithToolCalls(t *testing.T) {
	s := newTestSession(t, "m")
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "use tools"},
		{
			Role:    provider.RoleAssistant,
			Content: "let me check",
			ToolCalls: []provider.ToolCall{
				{
					ID:   "call_123",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "read_file", Arguments: `{"path": "test.txt"}`},
				},
			},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_123",
			Name:       "read_file",
			Content:    "file contents here",
		},
	}

	if err := s.Save("test-toolcalls"); err != nil {
		t.Fatal(err)
	}

	s2 := newTestSession(t, "m2")
	s2.SessionDir = s.SessionDir
	if err := s2.Load("test-toolcalls"); err != nil {
		t.Fatal(err)
	}

	if len(s2.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(s2.Messages))
	}
	assistant := s2.Messages[2]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "call_123" {
		t.Fatalf("tool call ID: got %q", assistant.ToolCalls[0].ID)
	}
	if assistant.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool call name: got %q", assistant.ToolCalls[0].Function.Name)
	}
	toolMsg := s2.Messages[3]
	if toolMsg.Role != provider.RoleTool {
		t.Fatalf("expected tool role, got %q", toolMsg.Role)
	}
	if toolMsg.Content != "file contents here" {
		t.Fatalf("tool content: got %q", toolMsg.Content)
	}
}

// TestSaveLoadRoundTripLargeMessages verifies large tool results survive.
func TestSaveLoadRoundTripLargeMessages(t *testing.T) {
	s := newTestSession(t, "m")
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "analyze this"},
		{Role: provider.RoleAssistant, Content: strings.Repeat("large payload ", 10000)},
	}

	if err := s.Save("test-large"); err != nil {
		t.Fatal(err)
	}

	s2 := newTestSession(t, "m2")
	s2.SessionDir = s.SessionDir
	if err := s2.Load("test-large"); err != nil {
		t.Fatal(err)
	}

	if len(s2.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(s2.Messages))
	}
	if len(s2.Messages[2].Content) != len(s.Messages[2].Content) {
		t.Fatalf("content length mismatch: %d vs %d", len(s2.Messages[2].Content), len(s.Messages[2].Content))
	}
}

// TestLoadNoSessionDir verifies error when SessionDir is not set.
func TestLoadNoSessionDir(t *testing.T) {
	s := newTestSession(t, "m")
	s.SessionDir = ""
	err := s.Load("anything")
	if err == nil {
		t.Fatal("expected error for missing SessionDir")
	}
}

// TestSaveNoSessionDir verifies error when SessionDir is not set.
func TestSaveNoSessionDir(t *testing.T) {
	s := newTestSession(t, "m")
	s.SessionDir = ""
	err := s.Save("anything")
	if err == nil {
		t.Fatal("expected error for missing SessionDir")
	}
}

// TestListNoSessionDir verifies error when SessionDir is not set.
func TestListNoSessionDir(t *testing.T) {
	s := newTestSession(t, "m")
	s.SessionDir = ""
	_, err := s.ListSessions()
	if err == nil {
		t.Fatal("expected error for missing SessionDir")
	}
}

// TestHasAutoSaveNoDir verifies HasAutoSave returns false when no dir.
func TestHasAutoSaveNoDir(t *testing.T) {
	s := newTestSession(t, "m")
	s.SessionDir = ""
	if s.HasAutoSave() {
		t.Fatal("expected false when no SessionDir")
	}
}

// --- helpers ---

func newTestSession(t *testing.T, model string) *Session {
	t.Helper()
	// Use a temp dir for session storage.
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, ".mivia", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{Model: model, SystemPrompt: "You are a helpful test assistant."}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	s.SessionDir = sessionDir
	return s
}

func findFirstUser(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			return m.Content
		}
	}
	return ""
}

func TestSaveAndLoadPreservesCreatedAt(t *testing.T) {
	s := newTestSession(t, "test-model")
	_, err := s.SendUser(context.Background(), "hello timed", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var userCreatedAt time.Time
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser && m.Content == "hello timed" {
			userCreatedAt = m.CreatedAt
			break
		}
	}
	if userCreatedAt.IsZero() {
		t.Fatal("expected CreatedAt set on user message after send")
	}
	if err := s.Save("test-created-at"); err != nil {
		t.Fatalf("save: %v", err)
	}
	s2 := newTestSession(t, "other")
	s2.SessionDir = s.SessionDir
	if err := s2.Load("test-created-at"); err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, m := range s2.Messages {
		if m.Role == provider.RoleUser && m.Content == "hello timed" {
			found = true
			if m.CreatedAt.IsZero() {
				t.Fatal("CreatedAt lost on reload")
			}
			// Compare with second precision (JSON time).
			if !m.CreatedAt.Equal(userCreatedAt) && m.CreatedAt.Unix() != userCreatedAt.Unix() {
				// Allow sub-second JSON rounding.
				diff := m.CreatedAt.Sub(userCreatedAt)
				if diff < 0 {
					diff = -diff
				}
				if diff > time.Second {
					t.Fatalf("CreatedAt mismatch: saved=%v loaded=%v", userCreatedAt, m.CreatedAt)
				}
			}
		}
	}
	if !found {
		t.Fatal("user message missing after load")
	}
}
