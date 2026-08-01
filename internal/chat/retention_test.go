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

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ---------------------------------------------------------------------------
// Per-turn snapshot retention (unbounded disk growth)
// ---------------------------------------------------------------------------

func turnTestMessages(n int) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: fmt.Sprintf("turn %d", n)},
		{Role: provider.RoleAssistant, Content: "ok"},
	}
}

// TestSaveAfterTurnReusesOneSnapshotDirectory pins the fix for per-turn
// snapshots minting a fresh directory (holding a full transcript copy) on
// every turn: a long session must not leave one directory per turn behind.
func TestSaveAfterTurnReusesOneSnapshotDirectory(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	const turns = 12
	for i := 0; i < turns; i++ {
		if err := mgr.SaveAfterTurn(turnTestMessages(i)); err != nil {
			t.Fatalf("SaveAfterTurn %d: %v", i, err)
		}
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		names := make([]string, 0, len(infos))
		for _, si := range infos {
			names = append(names, si.Name)
		}
		t.Fatalf("expected 1 rolling turn snapshot after %d turns, got %d: %v", turns, len(infos), names)
	}

	// Crash recovery still works: the surviving snapshot is the newest turn.
	loaded, err := store.Load(infos[0].Name)
	if err != nil {
		t.Fatalf("load turn snapshot: %v", err)
	}
	if len(loaded) == 0 || loaded[0].Content != fmt.Sprintf("turn %d", turns-1) {
		t.Fatalf("turn snapshot holds stale transcript: %+v", loaded)
	}
}

// TestPruneBoundsTurnSnapshots verifies stale per-turn directories left by
// earlier processes (or earlier versions) are pruned to TurnSaveKeep, while
// the most recent one - the crash-recovery copy - survives.
func TestPruneBoundsTurnSnapshots(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	// Simulate turn snapshots from previous runs, oldest first.
	var newest string
	for i := 0; i < TurnSaveKeep+5; i++ {
		name := fmt.Sprintf("%s%s20250115T1030%02d.000", AutoSaveName, turnSaveMarker, i)
		if err := store.Save(name, turnTestMessages(i), "m", "p"); err != nil {
			t.Fatal(err)
		}
		newest = name
	}

	if err := mgr.SaveOnExit(turnTestMessages(99)); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	turnCount := 0
	foundNewest := false
	for _, si := range infos {
		if !IsTurnSaveName(si.Name) {
			continue
		}
		turnCount++
		if si.Name == newest {
			foundNewest = true
		}
	}
	if turnCount > TurnSaveKeep {
		t.Fatalf("expected at most %d turn snapshots after prune, got %d", TurnSaveKeep, turnCount)
	}
	if !foundNewest {
		t.Fatalf("prune deleted the most recent turn snapshot %q (crash recovery lost)", newest)
	}
}

// TestSessionDiskDoesNotGrowPerTurn is the end-to-end view of the same defect:
// a real session directory with a real store must hold a bounded number of
// directories no matter how many turns are exchanged.
func TestSessionDiskDoesNotGrowPerTurn(t *testing.T) {
	s := newTestSession(t, "test-model")
	store, err := NewFileSessionStore(s.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSessionStore(store, NewSaveManager(store, "test-model", "fake"))

	const turns = 10
	for i := 0; i < turns; i++ {
		if _, err := s.SendUser(context.Background(), fmt.Sprintf("turn %d", i), io.Discard); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(s.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	dirs := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		}
	}
	if dirs > TurnSaveKeep {
		t.Fatalf("after %d turns the session dir holds %d directories (want <= %d)", turns, dirs, TurnSaveKeep)
	}

	// The snapshot must still carry the whole conversation for crash recovery.
	latest := s.LatestAutoSaveName()
	if latest == "" {
		t.Fatal("expected a turn snapshot for crash recovery")
	}
	msgs, err := store.Load(latest)
	if err != nil {
		t.Fatalf("load %q: %v", latest, err)
	}
	users := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			users++
		}
	}
	if users != turns {
		t.Fatalf("snapshot holds %d user turns, want %d", users, turns)
	}
}

// ---------------------------------------------------------------------------
// Reserved-prefix name collisions
// ---------------------------------------------------------------------------

// TestIsAutoSaveNameRejectsUserNames pins that a bare prefix match is not
// enough: "__last__mywork" is a user session, not a prune-eligible auto-save.
func TestIsAutoSaveNameRejectsUserNames(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{AutoSaveName, true},                           // legacy bare directory
		{AutoSaveName + "20250115T103000", true},       // legacy second-precision stamp
		{AutoSaveName + "20250115T103000.123", true},   // current millisecond stamp
		{AutoSaveName + "20250115T103000.123-2", true}, // collision suffix
		{AutoSaveName + turnSaveMarker + "20250115T103000", true},
		{AutoSaveName + "mywork", false},
		{AutoSaveName + "_foo", false},
		{AutoSaveName + "20250115", false},
		{AutoSaveName + "notatimestamp.000", false},
		{"my-session", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAutoSaveName(tt.name); got != tt.want {
			t.Errorf("IsAutoSaveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestUserSessionWithReservedPrefixSurvivesPrune is the end-to-end proof:
// saving "__last__mywork" and then exceeding the auto-save budget must not
// delete the user's session.
func TestUserSessionWithReservedPrefixSurvivesPrune(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")

	userName := AutoSaveName + "mywork"
	userMsgs := []provider.Message{
		{Role: provider.RoleUser, Content: "important work"},
		{Role: provider.RoleAssistant, Content: "noted"},
	}
	if err := store.Save(userName, userMsgs, "test-model", "test-provider"); err != nil {
		t.Fatal(err)
	}

	// Blow well past the exit auto-save budget.
	for i := 0; i < AutoSaveKeep+5; i++ {
		if err := mgr.SaveOnExit(turnTestMessages(i)); err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := store.Load(userName)
	if err != nil {
		t.Fatalf("user session %q was pruned: %v", userName, err)
	}
	if len(loaded) != 2 || loaded[0].Content != "important work" {
		t.Fatalf("user session content damaged: %+v", loaded)
	}
}

// ---------------------------------------------------------------------------
// Orphan recovery
// ---------------------------------------------------------------------------

func writeOrphanChunk(t *testing.T, dir, file string, msgs []provider.Message) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	if err := writeJSONL(path, msgs); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRecoverOrphanedSessionUsesContiguousChunks pins that recovery never
// writes a ChunkCount that Load cannot satisfy: Load reads chunk_0000..N-1 by
// index, so counting files rather than contiguous indices produces a session
// that is listed but fails to open.
func TestRecoverOrphanedSessionUsesContiguousChunks(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}

	// A gap after chunk_0001: only the contiguous prefix is recoverable.
	name := AutoSaveName + "20250115T103000.000"
	dir := filepath.Join(root, name)
	writeOrphanChunk(t, dir, "chunk_0000.jsonl", []provider.Message{{Role: provider.RoleUser, Content: "a"}})
	writeOrphanChunk(t, dir, "chunk_0001.jsonl", []provider.Message{{Role: provider.RoleAssistant, Content: "b"}})
	writeOrphanChunk(t, dir, "chunk_0003.jsonl", []provider.Message{{Role: provider.RoleAssistant, Content: "d"}})

	if !recoverOrphanedSession(dir) {
		t.Fatal("expected recovery of contiguous prefix to succeed")
	}
	msgs, err := store.Load(name)
	if err != nil {
		t.Fatalf("recovered session does not load: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("recovered %d messages, want 2 (contiguous prefix only)", len(msgs))
	}

	// The unreferenced chunk is data - recovery must not delete it.
	if _, err := os.Stat(filepath.Join(dir, "chunk_0003.jsonl")); err != nil {
		t.Fatalf("recovery destroyed non-contiguous chunk: %v", err)
	}
}

// TestRecoverOrphanedSessionWithoutFirstChunk verifies a directory with no
// chunk_0000 is left untouched rather than recorded as a loadable session.
func TestRecoverOrphanedSessionWithoutFirstChunk(t *testing.T) {
	root := t.TempDir()
	name := AutoSaveName + "20250115T103000.000"
	dir := filepath.Join(root, name)
	writeOrphanChunk(t, dir, "chunk_0003.jsonl", []provider.Message{{Role: provider.RoleUser, Content: "orphan"}})

	if recoverOrphanedSession(dir) {
		t.Fatal("expected recovery to decline a directory with no chunk_0000")
	}
	if _, err := os.Stat(filepath.Join(dir, metaFileName)); err == nil {
		t.Fatal("recovery wrote meta.json for an unloadable directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "chunk_0003.jsonl")); err != nil {
		t.Fatalf("recovery destroyed data it could not use: %v", err)
	}
}

// TestRecoverOrphanedSessionPreservesChunkTime pins that a recovered stale
// directory does not stamp itself as the newest session - otherwise resume
// restores the crashed leftovers instead of the genuinely latest session.
func TestRecoverOrphanedSessionPreservesChunkTime(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}

	// A genuinely current session.
	if err := store.Save("current", turnTestMessages(1), "m", "p"); err != nil {
		t.Fatal(err)
	}

	stale := AutoSaveName + "20250115T103000.000"
	dir := filepath.Join(root, stale)
	path := writeOrphanChunk(t, dir, "chunk_0000.jsonl", turnTestMessages(0))
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if !recoverOrphanedSession(dir) {
		t.Fatal("expected recovery to succeed")
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) == 0 || infos[0].Name != "current" {
		t.Fatalf("recovered stale session sorts ahead of the newest one: %+v", infos)
	}
	for _, si := range infos {
		if si.Name != stale {
			continue
		}
		if diff := si.UpdatedAt.Sub(old); diff > time.Second || diff < -time.Second {
			t.Fatalf("recovered UpdatedAt = %v, want the chunk mtime %v", si.UpdatedAt, old)
		}
	}
}

// TestPruneRecoversInterruptedSaves verifies the recovery path is actually
// reachable in the shipped CLI, which always wires a SaveManager.
func TestPruneRecoversInterruptedSaves(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewSaveManager(store, "test-model", "test-provider")

	orphan := AutoSaveName + "20250115T103000.000"
	writeOrphanChunk(t, filepath.Join(root, orphan), "chunk_0000.jsonl", []provider.Message{
		{Role: provider.RoleUser, Content: "survived"},
		{Role: provider.RoleAssistant, Content: "the crash"},
	})

	if err := mgr.SaveOnExit(turnTestMessages(0)); err != nil {
		t.Fatal(err)
	}

	msgs, err := store.Load(orphan)
	if err != nil {
		t.Fatalf("interrupted save was not recovered on exit: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "survived" {
		t.Fatalf("recovered content: %+v", msgs)
	}
}

// TestSaveManagerTurnSnapshotNamesAreDistinctPerManager guards the reason the
// rolling name is minted per manager rather than being a fixed constant: two
// processes sharing one workspace must not overwrite each other's snapshot.
func TestSaveManagerTurnSnapshotNamesAreDistinctPerManager(t *testing.T) {
	store := newTestStore(t)
	a := NewSaveManager(store, "m", "p")
	b := NewSaveManager(store, "m", "p")

	if err := a.SaveAfterTurn(turnTestMessages(1)); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAfterTurn(turnTestMessages(2)); err != nil {
		t.Fatal(err)
	}

	infos, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 distinct per-manager snapshots, got %d", len(infos))
	}
	for _, si := range infos {
		if !strings.Contains(si.Name, turnSaveMarker) {
			t.Fatalf("expected turn snapshot name, got %q", si.Name)
		}
	}
}
