package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ---------------------------------------------------------------------------
// FileSessionStore tests (interface contract)
// ---------------------------------------------------------------------------

func TestFileSessionStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi there"},
	}

	if err := store.Save("test-session", msgs, "test-model", "test-provider"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("test-session")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("message count: got %d, want 2", len(loaded))
	}
	if loaded[0].Content != "hello" {
		t.Fatalf("first msg: got %q, want %q", loaded[0].Content, "hello")
	}
	if loaded[1].Content != "hi there" {
		t.Fatalf("second msg: got %q, want %q", loaded[1].Content, "hi there")
	}
}

func TestFileSessionStore_SaveOverwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	msgs1 := []provider.Message{{Role: provider.RoleUser, Content: "v1"}}
	if err := store.Save("overwrite-test", msgs1, "", ""); err != nil {
		t.Fatal(err)
	}

	msgs2 := []provider.Message{
		{Role: provider.RoleUser, Content: "v2"},
		{Role: provider.RoleAssistant, Content: "response"},
	}
	if err := store.Save("overwrite-test", msgs2, "", ""); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("overwrite-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("after overwrite: got %d messages, want 2", len(loaded))
	}
	if loaded[0].Content != "v2" {
		t.Fatalf("expected overwritten content, got %q", loaded[0].Content)
	}
}

func TestFileSessionStore_List(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Save two sessions.
	msgs1 := []provider.Message{{Role: provider.RoleUser, Content: "first"}}
	if err := store.Save("session-a", msgs1, "", ""); err != nil {
		t.Fatal(err)
	}

	msgs2 := []provider.Message{{Role: provider.RoleUser, Content: "second"}}
	if err := store.Save("session-b", msgs2, "", ""); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(infos) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(infos))
	}

	// Verify both named sessions appear in the list with correct metadata.
	foundA, foundB := false, false
	for _, si := range infos {
		switch si.Name {
		case "session-a":
			foundA = true
			if si.TurnCount != 1 {
				t.Fatalf("session-a turn count: got %d, want 1", si.TurnCount)
			}
			if si.MessageCount != 1 {
				t.Fatalf("session-a message count: got %d, want 1", si.MessageCount)
			}
			if si.CreatedAt.IsZero() {
				t.Fatal("session-a CreatedAt is zero")
			}
			if si.UpdatedAt.IsZero() {
				t.Fatal("session-a UpdatedAt is zero")
			}
			if si.UpdatedAt.Before(si.CreatedAt) {
				t.Fatal("session-a UpdatedAt before CreatedAt")
			}
		case "session-b":
			foundB = true
			if si.TurnCount != 1 {
				t.Fatalf("session-b turn count: got %d, want 1", si.TurnCount)
			}
			if si.MessageCount != 1 {
				t.Fatalf("session-b message count: got %d, want 1", si.MessageCount)
			}
		}
	}
	if !foundA {
		t.Fatal("session-a not found in list")
	}
	if !foundB {
		t.Fatal("session-b not found in list")
	}
}

func TestFileSessionStore_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected empty list, got %d sessions", len(infos))
	}
}

func TestFileSessionStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	msgs := []provider.Message{{Role: provider.RoleUser, Content: "delete me"}}
	if err := store.Save("delete-test", msgs, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete("delete-test"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify it's gone.
	_, err = store.Load("delete-test")
	if err == nil {
		t.Fatal("expected error loading deleted session")
	}
}

func TestFileSessionStore_DeleteNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	err = store.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestFileSessionStore_LoadNotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestNewFileSessionStore_EmptyDir(t *testing.T) {
	_, err := NewFileSessionStore("")
	if err == nil {
		t.Fatal("expected error for empty directory")
	}
}

func TestFileSessionStore_Chunking(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create more than ChunkMessageThreshold messages.
	target := ChunkMessageThreshold + 50
	var msgs []provider.Message
	for i := 0; i < target; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "hello"})
		msgs = append(msgs, provider.Message{Role: provider.RoleAssistant, Content: "world"})
	}

	if err := store.Save("chunked", msgs, "", ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify multiple chunk files exist in the session directory.
	sessionDir := filepath.Join(dir, "chunked")
	entries, err := os.ReadDir(sessionDir)
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

	// Load and verify all messages preserved.
	loaded, err := store.Load("chunked")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != target*2 {
		t.Fatalf("message count: got %d, want %d", len(loaded), target*2)
	}
}

func TestFileSessionStore_Concurrent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Concurrent saves under different names should not conflict.
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			msgs := []provider.Message{
				{Role: provider.RoleUser, Content: "hello"},
				{Role: provider.RoleAssistant, Content: "world"},
			}
			name := "concurrent-" + string(rune('a'+n))
			if err := store.Save(name, msgs, "", ""); err != nil {
				t.Errorf("concurrent save %d: %v", n, err)
			}
			loaded, err := store.Load(name)
			if err != nil {
				t.Errorf("concurrent load %d: %v", n, err)
			}
			if len(loaded) != 2 {
				t.Errorf("concurrent %d: got %d messages, want 2", n, len(loaded))
			}
			done <- true
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) < 10 {
		t.Fatalf("expected at least 10 sessions after concurrent saves, got %d", len(infos))
	}
}

func TestFileSessionStore_ListIgnoresCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileSessionStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Save one valid session.
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "valid"}}
	if err := store.Save("valid", msgs, "", ""); err != nil {
		t.Fatal(err)
	}

	// Create a directory with no meta.json (corrupt session).
	corruptDir := filepath.Join(dir, "corrupt")
	if err := os.MkdirAll(corruptDir, 0o755); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Should only see the valid session.
	if len(infos) != 1 {
		t.Fatalf("expected 1 session (ignoring corrupt), got %d", len(infos))
	}
	if infos[0].Name != "valid" {
		t.Fatalf("expected 'valid', got %q", infos[0].Name)
	}
}

func TestLegacyImportRollbackAndIdempotency(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("legacy", []provider.Message{{Role: provider.RoleUser, Content: "bounded import"}}, "model", "provider"); err != nil {
		t.Fatal(err)
	}
	principal, err := contextstate.NewPrincipal("workspace", "imported", "subject")
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingImportSink{}
	importer, err := NewLegacyImporter(store, sink, contextstate.RedactionPolicy{Configured: true, Patterns: []string{"not-present"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := importer.Import(context.Background(), principal, "legacy", "operation-1")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	second, err := importer.Import(context.Background(), principal, "legacy", "operation-1")
	if err != nil {
		t.Fatalf("repeat import: %v", err)
	}
	if sink.calls != 1 || first.IdempotencyKey != second.IdempotencyKey || len(first.SourceMap) != 1 {
		t.Fatalf("calls=%d first=%+v second=%+v", sink.calls, first, second)
	}
	if err := store.Save("malformed", []provider.Message{{Role: "unsupported", Content: "bounded"}}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background(), principal, "malformed", "operation-2"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("malformed import error = %v, want ErrInvalidDTO", err)
	}
	if sink.calls != 1 {
		t.Fatalf("malformed import reached sink, calls=%d", sink.calls)
	}
}

type recordingImportSink struct {
	calls int
}

func (s *recordingImportSink) ImportSource(_ context.Context, principal contextstate.Principal, legacyID, key string, events []contextstate.SourceEvent, _ []contextstate.PayloadRecord) (contextstate.ImportResult, error) {
	s.calls++
	start := events[0].ID
	end := events[len(events)-1].ID
	rng, _ := contextstate.NewSourceRange(start, end)
	return contextstate.ImportResult{SessionID: principal.SessionID, SourceRange: rng, IdempotencyKey: key, Status: "imported", Imported: len(events), SourceMap: []contextstate.SourceMapping{{LegacyID: legacyID, SessionID: principal.SessionID, SourceStart: start, SourceEnd: end}}}, nil
}
