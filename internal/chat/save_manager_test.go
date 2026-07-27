package chat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

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

	// Verify SaveAfterTurn does NOT prune (saving 2 more only adds to list).
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
	if len(infos2) != 2 {
		t.Fatalf("expected 2 sessions after 2 SaveAfterTurn calls (no prune), got %d", len(infos2))
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

	// SaveOnExit should prune only the exit auto-saves.
	// Since SaveAfterTurn uses _turn_ prefix, and SaveOnExit uses a
	// different prefix, the exit save won't prune turn saves.
	if err := mgr.SaveOnExit(msgs); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}

	// With the fix, prune filters out names containing "_turn_",
	// so turn-saves are preserved. Only exit auto-saves get pruned.
	// Since there's only 1 exit save (<= AutoSaveKeep), nothing is deleted.
	// Expected: all 55 turn saves + 1 exit save = 56 sessions.
	expectedTotal := AutoSaveKeep + 5 + 1 // 56
	if len(infos) != expectedTotal {
		t.Fatalf("expected exactly %d sessions after pruning (all turn-saves preserved + exit save), got %d",
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

	// No meta.json — this is a "corrupted" orphan.
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
