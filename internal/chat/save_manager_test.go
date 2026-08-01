package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func TestOlderAutosaveCannotOverwriteNewerRevision(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "model", "provider")
	binding := BindingFence{ProviderName: "provider", Model: "model", ModelGeneration: 1}
	older := OperationToken{Epoch: 1, Revision: contextstate.Revision{Session: 1, Durable: 1}, Binding: binding, TurnID: 1}
	newer := older
	newer.Epoch = 2
	newer.Revision.Session = 2
	newer.Revision.Durable = 2
	newer.TurnID = 2
	newMessages := []provider.Message{{Role: provider.RoleUser, Content: "new"}, {Role: provider.RoleAssistant, Content: "new answer"}}
	if err := mgr.SaveAfterTurnWithRevision(newMessages, newer); err != nil {
		t.Fatalf("newer autosave: %v", err)
	}
	oldMessages := []provider.Message{{Role: provider.RoleUser, Content: "old"}, {Role: provider.RoleAssistant, Content: "old answer"}}
	if err := mgr.SaveAfterTurnWithRevision(oldMessages, older); !errors.Is(err, ErrStaleAutosave) {
		t.Fatalf("older autosave error = %v, want ErrStaleAutosave", err)
	}
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("autosave count = %d, want 1", len(infos))
	}
	got, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Content != "new" {
		t.Fatalf("stale autosave overwrote newer content: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// SaveManager tests
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) *FileSessionStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveManager_SaveAfterTurn(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi"},
	}

	if err := mgr.SaveAfterTurn(msgs); err != nil {
		t.Fatalf("SaveAfterTurn: %v", err)
	}

	// Should have created a session with _turn_ in the name.
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}

	if !IsAutoSaveName(infos[0].Name) {
		t.Fatalf("expected auto-save name, got %q", infos[0].Name)
	}

	// Turn snapshots roll in place: a second turn overwrites the same
	// directory rather than minting another full transcript copy.
	msgs2 := []provider.Message{
		{Role: provider.RoleUser, Content: "more"},
		{Role: provider.RoleAssistant, Content: "data"},
	}
	if err := mgr.SaveAfterTurn(msgs2); err != nil {
		t.Fatal(err)
	}

	infos2, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos2) != 1 {
		t.Fatalf("expected 1 rolling turn snapshot after 2 SaveAfterTurn calls, got %d", len(infos2))
	}
	loaded, err := store.Load(infos2[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Content != "more" {
		t.Fatalf("rolling snapshot should hold the newest turn, got %+v", loaded)
	}
}

func TestSaveManager_SaveAfterTurn_EmptyContent(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	// Empty messages should be no-op.
	if err := mgr.SaveAfterTurn(nil); err != nil {
		t.Fatalf("SaveAfterTurn(nil): %v", err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected 0 sessions after SaveAfterTurn(nil), got %d", len(infos))
	}

	// Only system prompt should also be no-op.
	sysMsgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
	}
	if err := mgr.SaveAfterTurn(sysMsgs); err != nil {
		t.Fatalf("SaveAfterTurn(system only): %v", err)
	}
	infos2, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos2) != 0 {
		t.Fatalf("expected 0 sessions after system-only, got %d", len(infos2))
	}
}

func TestSaveManager_SaveOnExit(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "final message"},
		{Role: provider.RoleAssistant, Content: "goodbye"},
	}

	if err := mgr.SaveOnExit(msgs); err != nil {
		t.Fatalf("SaveOnExit: %v", err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 session after SaveOnExit, got %d", len(infos))
	}
	if !IsAutoSaveName(infos[0].Name) {
		t.Fatalf("expected auto-save name, got %q", infos[0].Name)
	}
}

func TestSaveManager_SaveOnExit_PrunesOld(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	// Save many turn snapshots (more than AutoSaveKeep).
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "data"},
		{Role: provider.RoleAssistant, Content: "response"},
	}
	for i := 0; i < AutoSaveKeep+5; i++ {
		if err := mgr.SaveAfterTurn(msgs); err != nil {
			t.Fatal(err)
		}
	}

	if err := mgr.SaveOnExit(msgs); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	// Turn saves all roll through one directory, and the single exit save is
	// well under AutoSaveKeep, so nothing is pruned: 1 turn + 1 exit = 2.
	expectedTotal := 2
	if len(infos) != expectedTotal {
		t.Fatalf("expected exactly %d sessions (1 rolling turn snapshot + 1 exit save), got %d",
			expectedTotal, len(infos))
	}
}

func TestSaveManager_Metrics(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "test"},
		{Role: provider.RoleAssistant, Content: "response"},
	}

	// Do a few saves.
	_ = mgr.SaveAfterTurn(msgs)
	_ = mgr.SaveAfterTurn(msgs)
	_ = mgr.SaveOnExit(msgs)

	metrics := mgr.Metrics()
	if metrics.SaveAfterTurnCount != 2 {
		t.Fatalf("SaveAfterTurnCount: got %d, want 2", metrics.SaveAfterTurnCount)
	}
	if metrics.SaveOnExitCount != 1 {
		t.Fatalf("SaveOnExitCount: got %d, want 1", metrics.SaveOnExitCount)
	}
}

func TestSaveManager_Concurrent(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "concurrent"},
		{Role: provider.RoleAssistant, Content: "test"},
	}

	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func() {
			if err := mgr.SaveAfterTurn(msgs); err != nil {
				t.Errorf("concurrent SaveAfterTurn: %v", err)
			}
			done <- true
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	metrics := mgr.Metrics()
	if metrics.SaveAfterTurnCount != 20 {
		t.Fatalf("expected 20 saves, got %d", metrics.SaveAfterTurnCount)
	}
}

func TestSaveManager_OrphanRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create an interrupted save: chunk files but no meta.json.
	orphanDir := filepath.Join(dir, AutoSaveName+"_orphan")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a chunk file.
	chunkPath := filepath.Join(orphanDir, "chunk_0000.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "survived"},
		{Role: provider.RoleAssistant, Content: "the crash"},
	}
	if err := writeJSONL(chunkPath, msgs); err != nil {
		t.Fatal(err)
	}

	// No meta.json - this is a "corrupted" orphan.
	// List should skip it.
	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, si := range infos {
		if si.Name == AutoSaveName+"_orphan" {
			t.Fatal("orphan should not appear in List without meta.json")
		}
	}

	// Now recover it.
	recovered := recoverOrphanedSession(orphanDir)
	if !recovered {
		t.Fatal("expected orphan recovery to succeed")
	}

	// Now it should appear in list.
	infos2, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, si := range infos2 {
		if si.Name == AutoSaveName+"_orphan" {
			found = true
			if si.MessageCount != 2 {
				t.Fatalf("recovered session: expected 2 messages, got %d", si.MessageCount)
			}
		}
	}
	if !found {
		t.Fatal("recovered session should appear in List")
	}

	// Load verified content.
	loaded, err := store.Load(AutoSaveName + "_orphan")
	if err != nil {
		t.Fatalf("Load recovered session: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Content != "survived" {
		t.Fatalf("recovered content: got %q, want %q", loaded[0].Content, "survived")
	}
}

// ---------------------------------------------------------------------------
// hasContent unit tests
// ---------------------------------------------------------------------------

func TestHasContent_EmptyMessages(t *testing.T) {
	if hasContent(nil) {
		t.Error("hasContent(nil) = true, want false")
	}
	if hasContent([]provider.Message{}) {
		t.Error("hasContent([]) = true, want false")
	}
}

func TestHasContent_SystemOnlyMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
	}
	if hasContent(msgs) {
		t.Error("hasContent(system only) = true, want false")
	}
}

func TestHasContent_SystemPlusUser(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleUser, Content: "hello"},
	}
	if !hasContent(msgs) {
		t.Error("hasContent(system+user) = false, want true")
	}
}

func TestHasContent_SystemPlusAssistant(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a helpful assistant."},
		{Role: provider.RoleAssistant, Content: "hi there"},
	}
	if !hasContent(msgs) {
		t.Error("hasContent(system+assistant) = false, want true")
	}
}

func TestHasContent_UserOnly(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "just a user message"},
	}
	if !hasContent(msgs) {
		t.Error("hasContent(user only) = false, want true")
	}
}

func TestHasContent_AssistantOnly(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: "just an assistant message"},
	}
	if !hasContent(msgs) {
		t.Error("hasContent(assistant only) = false, want true")
	}
}

func TestHasContent_MixedMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "user"},
		{Role: provider.RoleAssistant, Content: "assistant"},
		{Role: provider.RoleUser, Content: "follow-up"},
	}
	if !hasContent(msgs) {
		t.Error("hasContent(mixed) = false, want true")
	}
}
