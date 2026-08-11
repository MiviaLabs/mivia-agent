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
	oldMsgs := chunkMsgsForCount(2, "old")
	if err := store.Save("s", oldMsgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Dir(), "s")
	oldChunk0, err := os.ReadFile(filepath.Join(dir, "chunk_0000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
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
	assertSaveFailsWith(t, store, "s", chunkMsgsForCount(2, "new"), injectedErr)

	// The previous snapshot must still be intact and loadable: meta still
	// references two chunks, every referenced chunk file still exists, and the
	// loaded transcript is byte-for-byte the old one.
	assertFailedSaveLeavesSnapshot(t, store, "s", dir, oldMsgs, 2, oldChunk0, oldChunk1)
}

// assertSaveFailsWith re-saves msgs under name and asserts the save fails with
// exactly wantErr (the injected rename failure).
func assertSaveFailsWith(t *testing.T, store *FileSessionStore, name string, msgs []provider.Message, wantErr error) {
	t.Helper()
	if err := store.Save(name, msgs, "m", "p"); err == nil {
		t.Fatal("expected the injected rename failure, got nil")
	} else if !errors.Is(err, wantErr) {
		t.Fatalf("expected the injected rename failure, got: %v", err)
	}
}

// assertFailedSaveLeavesSnapshot asserts that a failed mid-swap save left the
// previous snapshot byte-for-byte intact: meta still references wantChunks,
// every message round-trips in order, and each chunk file still holds its old
// content. The chunk_0000 comparison is the assertion that fails pre-fix: the
// first chunk was already swapped to new content before the injected failure,
// so pre-fix it held NEW content while meta.json still referenced the old
// snapshot, silently corrupting the load.
func assertFailedSaveLeavesSnapshot(t *testing.T, store *FileSessionStore, name, dir string, wantMsgs []provider.Message, wantChunks int, wantChunk0, wantChunk1 []byte) {
	t.Helper()
	loaded, info, err := store.LoadWithInfo(name)
	if err != nil {
		t.Fatalf("load after failed save must still succeed: %v", err)
	}
	if info.ChunkCount != wantChunks {
		t.Fatalf("ChunkCount: got %d, want %d", info.ChunkCount, wantChunks)
	}
	if len(loaded) != len(wantMsgs) {
		t.Fatalf("loaded messages: got %d, want %d", len(loaded), len(wantMsgs))
	}
	for i, want := range wantMsgs {
		if loaded[i].Role != want.Role || loaded[i].Content != want.Content {
			t.Fatalf("message %d = %+v, want %+v: failed save must leave the previous snapshot byte-for-byte intact", i, loaded[i], want)
		}
	}
	// Both chunks must still hold their old content: chunk_0001 was never
	// swapped (its rename failed) and chunk_0000 must have been restored
	// (pre-fix it held NEW content, corrupting the load).
	after0, err := os.ReadFile(filepath.Join(dir, "chunk_0000.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after0) != string(wantChunk0) {
		t.Fatal("chunk_0000.jsonl changed after the failed save (pre-fix it held new content)")
	}
	after, err := os.ReadFile(filepath.Join(dir, "chunk_0001.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(wantChunk1) {
		t.Fatal("chunk_0001.jsonl changed after the failed save")
	}
	// No staged temp files or backups may remain.
	for _, pattern := range []string{"*.tmp", "*.bak"} {
		leftovers, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(leftovers) > 0 {
			t.Fatalf("%s files left behind: %v", pattern, leftovers)
		}
	}
}

// chunkMsgsForCount builds a transcript sized to span n chunk files, labeled
// with prefix so old and new snapshots are distinguishable.
func chunkMsgsForCount(n int, prefix string) []provider.Message {
	count := 3
	if n > 1 {
		count = ChunkMessageThreshold + 1
	}
	msgs := make([]provider.Message, 0, count)
	for i := 0; i < count; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("%s-%d", prefix, i)})
	}
	return msgs
}

// A re-save that fails mid-swap must restore the FULL previous snapshot, not
// just the chunk whose rename failed: the old code renamed staged chunks over
// their destinations one by one, so a failure at commit index i left chunks
// 0..i-1 holding NEW content while meta.json still referenced the old chunk
// count - a mixed transcript that loads without error and corrupts the
// conversation. The matrix sweeps every reachable size delta (same, grow,
// shrink) and failure index so the rollback path is pinned for all of them.
func TestChunkSwapRenameFailureRestoresFullPreviousSnapshot(t *testing.T) {
	tests := []struct {
		name       string
		oldChunks  int
		newChunks  int
		failCommit int
	}{
		{name: "same-size/fail-commit-0", oldChunks: 2, newChunks: 2, failCommit: 0},
		{name: "same-size/fail-commit-1", oldChunks: 2, newChunks: 2, failCommit: 1},
		{name: "grow/fail-commit-0", oldChunks: 1, newChunks: 2, failCommit: 0},
		{name: "grow/fail-commit-1", oldChunks: 1, newChunks: 2, failCommit: 1},
		{name: "shrink/fail-commit-0", oldChunks: 2, newChunks: 1, failCommit: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewFileSessionStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			oldMsgs := chunkMsgsForCount(tc.oldChunks, "old")
			if err := store.Save("s", oldMsgs, "m", "p"); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(store.Dir(), "s")

			injectedErr := errors.New("injected rename failure")
			origRename := renameFile
			renameFile = func(oldpath, newpath string) error {
				if strings.HasSuffix(newpath, fmt.Sprintf(chunkFileName, tc.failCommit)) {
					return injectedErr
				}
				return origRename(oldpath, newpath)
			}
			t.Cleanup(func() { renameFile = origRename })

			newMsgs := chunkMsgsForCount(tc.newChunks, "new")
			if err := store.Save("s", newMsgs, "m", "p"); !errors.Is(err, injectedErr) {
				t.Fatalf("save error = %v, want the injected rename failure", err)
			}

			loaded, info, err := store.LoadWithInfo("s")
			if err != nil {
				t.Fatalf("load after failed save must still succeed: %v", err)
			}
			if info.ChunkCount != tc.oldChunks {
				t.Fatalf("ChunkCount: got %d, want %d", info.ChunkCount, tc.oldChunks)
			}
			if len(loaded) != len(oldMsgs) {
				t.Fatalf("loaded messages: got %d, want %d", len(loaded), len(oldMsgs))
			}
			for i, want := range oldMsgs {
				if loaded[i].Role != want.Role || loaded[i].Content != want.Content {
					t.Fatalf("message %d = %+v, want %+v: failed save must leave the previous snapshot byte-for-byte intact", i, loaded[i], want)
				}
			}
			for _, pattern := range []string{"*.tmp", "*.bak"} {
				leftovers, _ := filepath.Glob(filepath.Join(dir, pattern))
				if len(leftovers) > 0 {
					t.Fatalf("%s files left behind after failed save: %v", pattern, leftovers)
				}
			}
		})
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
