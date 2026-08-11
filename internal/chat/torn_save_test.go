package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func callMsg(id, name string) provider.Message {
	var c provider.ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = "{}"
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{c}}
}

// A history whose trailing tool results were lost - a torn chunk rewrite leaves
// exactly this shape, because writeJSONL truncates in place and readJSONL stops
// at the last complete line without error. The assistant message still announces
// a tool_call whose result is gone, so the API rejects every subsequent turn and
// the session is unusable with no way to recover through the UI.
func TestLoadRepairsOrphanedToolCalls(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	sess.SessionDir = t.TempDir()
	sess.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		callMsg("c1", "read_file"),
		{Role: provider.RoleTool, ToolCallID: "c1", Name: "read_file", Content: "data"},
	}
	if err := sess.Save("torn"); err != nil {
		t.Fatal(err)
	}

	// Simulate the torn write: drop the trailing tool-result line.
	chunk := filepath.Join(sess.SessionDir, "torn", "chunk_0000.jsonl")
	raw, err := os.ReadFile(chunk)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if err := os.WriteFile(chunk, []byte(strings.Join(lines[:2], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	reloaded.SessionDir = sess.SessionDir
	if err := reloaded.Load("torn"); err != nil {
		t.Fatalf("load: %v", err)
	}

	announced := map[string]bool{}
	answered := map[string]bool{}
	for _, m := range reloaded.Messages {
		for _, c := range m.ToolCalls {
			announced[c.ID] = true
		}
		if m.Role == provider.RoleTool {
			answered[m.ToolCallID] = true
		}
	}
	for id := range announced {
		if !answered[id] {
			t.Fatalf("loaded history has an orphaned tool_call %q; it will be rejected on every turn: %+v", id, reloaded.Messages)
		}
	}
}

// A chunk rewrite that fails partway must not destroy the previous state: the
// old chunks were deleted before the new ones were written, so an ENOSPC/EIO
// mid-write left meta.json pointing at chunks that no longer exist and the
// session could never be loaded again.
func TestSaveDoesNotDestroyOldChunksBeforeWritingNew(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	sess.SessionDir = t.TempDir()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "first"}}
	if err := sess.Save("s"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(sess.SessionDir, "s")
	before, err := filepath.Glob(filepath.Join(dir, "chunk_*.jsonl"))
	if err != nil || len(before) == 0 {
		t.Fatalf("no chunks written: %v %v", before, err)
	}
	// Re-save with new content; the old chunk must never be absent while the
	// new one is missing. Assert the post-state is complete and loadable.
	sess.Messages = append(sess.Messages, provider.Message{Role: provider.RoleAssistant, Content: "second"})
	if err := sess.Save("s"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "x"})
	reloaded.SessionDir = sess.SessionDir
	if err := reloaded.Load("s"); err != nil {
		t.Fatalf("resave must leave a loadable session: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(reloaded.Messages))
	}
	// No temp files left behind.
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// A re-save that fails mid-swap must not destroy the previous snapshot: the
// old chunks were deleted before the staged ones were renamed into place, so a
// rename failure on the second commit left meta.json referencing a chunk file
// that no longer existed and the session could never be loaded again.
func TestChunkSwapRenameFailureKeepsOldSnapshot(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// A two-chunk transcript.
	oldMsgs := make([]provider.Message, 0, ChunkMessageThreshold+1)
	for i := 0; i < ChunkMessageThreshold+1; i++ {
		oldMsgs = append(oldMsgs, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("old-%d", i)})
	}
	if err := store.Save("s", oldMsgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Dir(), "s")
	oldChunk1, err := os.ReadFile(filepath.Join(dir, "chunk_0001.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	// Inject a rename failure on the second commit rename (chunk_0001).
	injectedErr := errors.New("injected rename failure")
	origRename := renameFile
	renameFile = func(oldpath, newpath string) error {
		if strings.HasSuffix(newpath, "chunk_0001.jsonl") {
			return injectedErr
		}
		return origRename(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = origRename })

	// Re-save a same-size transcript; the save must fail with the injected
	// error.
	newMsgs := make([]provider.Message, 0, ChunkMessageThreshold+1)
	for i := 0; i < ChunkMessageThreshold+1; i++ {
		newMsgs = append(newMsgs, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("new-%d", i)})
	}
	if err := store.Save("s", newMsgs, "m", "p"); err == nil {
		t.Fatal("expected the injected rename failure, got nil")
	} else if !errors.Is(err, injectedErr) {
		t.Fatalf("expected the injected rename failure, got: %v", err)
	}

	// The previous snapshot must still be intact and loadable: meta still
	// references two chunks and every referenced chunk file still exists.
	loaded, info, err := store.LoadWithInfo("s")
	if err != nil {
		t.Fatalf("load after failed save must still succeed: %v", err)
	}
	if info.ChunkCount != 2 {
		t.Fatalf("ChunkCount: got %d, want 2", info.ChunkCount)
	}
	if len(loaded) != ChunkMessageThreshold+1 {
		t.Fatalf("loaded messages: got %d, want %d", len(loaded), ChunkMessageThreshold+1)
	}
	// The chunk whose rename failed must still hold the old content.
	after, err := os.ReadFile(filepath.Join(dir, "chunk_0001.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(oldChunk1) {
		t.Fatal("chunk_0001.jsonl changed after the failed save")
	}
	// No staged temp files may remain.
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// Shrinking a session must commit meta.json to the new chunk count before the
// stale high-index chunks are removed: after a successful save the on-disk
// state matches the new count exactly, with no orphaned chunks left behind.
func TestShrinkSaveRemovesStaleChunkOnlyAfterCommit(t *testing.T) {
	store, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Two-chunk save.
	big := make([]provider.Message, 0, ChunkMessageThreshold+1)
	for i := 0; i < ChunkMessageThreshold+1; i++ {
		big = append(big, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("big-%d", i)})
	}
	if err := store.Save("s", big, "m", "p"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Dir(), "s")
	if _, err := os.Stat(filepath.Join(dir, "chunk_0001.jsonl")); err != nil {
		t.Fatalf("expected two chunks after the two-chunk save: %v", err)
	}

	// Shrink re-save: a single chunk.
	small := []provider.Message{{Role: provider.RoleUser, Content: "only"}}
	if err := store.Save("s", small, "m", "p"); err != nil {
		t.Fatal(err)
	}

	loaded, info, err := store.LoadWithInfo("s")
	if err != nil {
		t.Fatalf("load after shrink: %v", err)
	}
	if info.ChunkCount != 1 {
		t.Fatalf("ChunkCount: got %d, want 1", info.ChunkCount)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded messages: got %d, want 1", len(loaded))
	}
	// The stale high-index chunk is gone only after the save committed.
	if _, err := os.Stat(filepath.Join(dir, "chunk_0001.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("stale chunk_0001.jsonl should be gone after the shrink save, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chunk_0000.jsonl")); err != nil {
		t.Fatalf("chunk_0000.jsonl should exist: %v", err)
	}
}
