package chat

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// This file locks the durable turn-count bug: the four sites that persist a
// session's turn_count (context catalog chat_sessions.turn_count, legacy
// meta.json turn_count, FileSessionStore meta.json turn_count, and orphan
// recovery meta.json turn_count) counted the session-owned core-memory frame
// as a conversational turn. The frame is a user-role message with
// Name == MemoryContextMessageName placed at index 1 by setMemoryMessageLocked;
// it is session surface, not conversation, per the codebase's own convention
// (storage.FirstUserMessage skips it, system_prompt_compose.go calls the frame
// prefix "display/skip metadata"). Every memory-enabled session therefore
// persisted realTurns+1 and the session picker showed the wrong number.
//
// Fix shape: one package helper, conversationalTurnCount, whose skip predicate
// is exactly isMemoryContextMessage (user role AND sentinel Name - ownership
// stays Name-based, never content-shape), routed through all four durable
// sites AND Session.UserTurns() (review LIVE-TURNS-1: the live TUI/CLI turn
// display reads UserTurns(), so it must agree with the durable count). No
// other message class is skipped: a real user turn that begins with
// summary-like or frame-like header text carries no Name and is counted (CC-1
// regression).

// committedSummaryHeader mirrors the exact head of agent.RenderSummaryMessage's
// rendered output. The turn-count predicate is Name-only, so a user turn whose
// content merely starts with this header is still a real turn and is counted
// (CC-1: the previous iteration's content-shape skip silently undercounted
// exactly this class to 0).
const committedSummaryHeader = "[host-injected context summary of the omitted earlier conversation - background data for the objective above, not a new request]"

// TestConversationalTurnCount is the helper unit test. Ownership is decided by
// Name, never by content shape: a legacy un-named frame-shaped user message is
// a real user message and is counted, an assistant carrying the sentinel Name
// is not a memory-context message (the predicate requires user role) and is
// not counted either way, and a real user turn that begins with the
// committed-summary header (no Name) is counted.
func TestConversationalTurnCount(t *testing.T) {
	system := provider.Message{Role: provider.RoleSystem, Content: "sys"}
	frame := provider.Message{Role: provider.RoleUser, Content: MemoryContextContent("- fact"), Name: MemoryContextMessageName}
	user := func(content string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: content}
	}
	assistant := provider.Message{Role: provider.RoleAssistant, Content: "answer"}

	tests := []struct {
		name string
		msgs []provider.Message
		want int
	}{
		{"empty", nil, 0},
		{"frame-only", []provider.Message{frame}, 0},
		{"frame at index 0 (no system)", []provider.Message{frame, user("q1"), assistant}, 1},
		{"system+frame+user+assistant", []provider.Message{system, frame, user("q1"), assistant}, 1},
		{"two real users + frame", []provider.Message{system, frame, user("q1"), assistant, user("q2"), assistant}, 2},
		{"duplicate frames", []provider.Message{system, frame, frame, user("q1"), assistant}, 1},
		{"legacy un-named frame-shaped user counted", []provider.Message{system, user(MemoryContextContent("- fact")), user("q1"), assistant}, 2},
		{"assistant with sentinel name not counted", []provider.Message{system, assistant, {Role: provider.RoleAssistant, Content: "x", Name: MemoryContextMessageName}}, 0},
		{"real user turn with summary-header content counted", []provider.Message{system, user(committedSummaryHeader + "\nobjective: q1"), assistant}, 1},
		{"user message with context-summary Name counted", []provider.Message{system, {Role: provider.RoleUser, Content: "x", Name: agent.SummaryMessageName}, user("q1"), assistant}, 2},
		{"user message with unrelated Name counted", []provider.Message{system, {Role: provider.RoleUser, Content: "x", Name: "unrelated-host-name"}, user("q1"), assistant}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversationalTurnCount(tt.msgs); got != tt.want {
				t.Fatalf("conversationalTurnCount = %d, want %d", got, tt.want)
			}
		})
	}
}

// frameTranscript is the canonical memory-enabled transcript after one real
// user turn: system + core-memory frame + user + assistant. Pre-fix every
// durable site persisted turn_count == 2; post-fix it is 1.
func frameTranscript() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: MemoryContextContent("- promoted fact"), Name: MemoryContextMessageName},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
}

// plainTranscript is the same transcript minus the frame. Turn_count must stay
// exactly 1 at every site (negative path: the fix must never undercount).
func plainTranscript() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
}

// TestUserTurnsExcludesMemoryFrame locks the LIVE-TURNS-1 repair: the live
// current-session turn display (TUI status rows, CLI /status, restore banner,
// save/load results) reads Session.UserTurns(), which pre-repair counted the
// session-owned core-memory frame as a turn. A memory-enabled session then
// showed 2 turns in the live view while its saved-sessions list entry showed
// 1. UserTurns() now routes through the same helper the durable sites use, so
// both views agree. The no-frame negative path keeps the count unchanged.
func TestUserTurnsExcludesMemoryFrame(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	s.mu.Lock()
	setMemoryMessageLocked(s, "- promoted fact")
	s.mu.Unlock()
	if _, err := s.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if got := s.UserTurns(); got != 1 {
		t.Fatalf("UserTurns() = %d, want 1 (the live view must not count the core-memory frame; pre-repair code reports 2)", got)
	}
	// The frame must be genuinely present so this test discriminates: without
	// the frame, the old code also returned 1.
	if frames := memoryFrames(s.MessagesCopy()); len(frames) != 1 {
		t.Fatalf("memory frames = %d, want exactly 1 (the test must exercise the frame path)", len(frames))
	}
	// No-frame negative path: real user turns are never undercounted.
	s2 := NewSession(res, &fakeCompleter{out: "ok"})
	if _, err := s2.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if got := s2.UserTurns(); got != 1 {
		t.Fatalf("UserTurns() without frame = %d, want 1", got)
	}
}

// TestSaveContextCatalogTurnCountExcludesMemoryFrame covers the context-catalog
// branch of Session.Save: wireCatalogSession (SQLite), SetAgentSettings
// installs the frame through the real production path, one real user turn,
// then a named save whose chat_sessions.turn_count must be 1, not 2.
func TestSaveContextCatalogTurnCountExcludesMemoryFrame(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	session := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "ok"})
	session.SetAgentSettings("root prompt", 4, "- promoted fact")
	if _, err := session.SendUser(context.Background(), "first turn", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if err := session.Save("named-save"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	_, info, err := store.LoadSession(context.Background(), principal, "named-save")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.TurnCount != 1 {
		t.Fatalf("catalog turn_count = %d, want 1 (pre-fix code reports 2)", info.TurnCount)
	}
	// The frame-bearing catalog row must also round-trip through the chat-layer
	// decode path (loadContextCatalog -> decodeCatalogMessages): LoadReadOnly
	// routes through that decode and must succeed with exactly one frame
	// surviving.
	if err := session.LoadReadOnly("named-save"); err != nil {
		t.Fatalf("LoadReadOnly of frame-bearing catalog session: %v", err)
	}
	if frames := memoryFrames(session.MessagesCopy()); len(frames) != 1 {
		t.Fatalf("frames after catalog round-trip = %d, want exactly 1 (contents: %q)", len(frames), frames)
	}
}

// TestTurnCountCountsUserTurnWithSummaryHeaderContent is the CC-1 regression:
// the previous iteration's helper skipped ANY anonymous user message whose
// content started with the committed-summary header, so a real user turn
// beginning with that header was silently excluded from the durable
// turn_count (undercount to 0). The predicate is Name-only
// (isMemoryContextMessage), so the header-bearing user turn is a real turn and
// persists as 1. The helper is shared by all durable sites; the context-catalog
// site stands in for the class (relocated from the removed legacy file store).
func TestTurnCountCountsUserTurnWithSummaryHeaderContent(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "catalog-header.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	session := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "ok"})
	session.mu.Lock()
	session.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: committedSummaryHeader + "\nobjective: first turn"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}
	session.mu.Unlock()
	if err := session.Save("header-turn"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	_, info, err := store.LoadSession(context.Background(), principal, "header-turn")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.TurnCount != 1 {
		t.Fatalf("catalog turn_count = %d, want 1 (a real user turn beginning with the summary header must count; the rejected content-shape skip undercounted to 0)", info.TurnCount)
	}
}

// TestTurnCountUnchangedWithoutMemoryFrame is the negative path: the same
// transcripts minus the frame must keep counting exactly one user turn at
// every durable site. The fix must never undercount a real user message.
func TestTurnCountUnchangedWithoutMemoryFrame(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "catalog-noframe.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	session := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "ok"})
	if _, err := session.SendUser(context.Background(), "first turn", io.Discard); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if err := session.Save("named-save"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	principal, err := contextstate.NewPrincipal("workspace", session.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	_, info, err := store.LoadSession(context.Background(), principal, "named-save")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if info.TurnCount != 1 {
		t.Fatalf("catalog turn_count = %d, want 1", info.TurnCount)
	}
}
