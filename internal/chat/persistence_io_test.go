package chat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWriteMetaJSONSyncsBeforeClose verifies that writeMetaJSON calls Sync
// before Close. This is the regression test for the torn-write bug where
// metadata could exist only in the OS page cache at the time of rename.
func TestWriteMetaJSONSyncsBeforeClose(t *testing.T) {
	dir := t.TempDir()

	var syncCalled bool
	original := syncFile
	syncFile = func(f *os.File) error {
		syncCalled = true
		return nil
	}
	t.Cleanup(func() { syncFile = original })

	now := time.Now().Truncate(time.Millisecond)
	meta := sessionMeta{
		Name:         "sync-test",
		Model:        "test-model",
		Provider:     "test-provider",
		CreatedAt:    now,
		UpdatedAt:    now,
		TurnCount:    2,
		TokenCount:   50,
		ChunkCount:   1,
		MessageCount: 4,
		Dir:          dir,
		Worktree:     "main",
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		t.Fatalf("writeMetaJSON: %v", err)
	}

	if !syncCalled {
		t.Fatal("writeMetaJSON did not call Sync before Close; torn-write bug not fixed")
	}

	// Verify meta.json exists and round-trips correctly.
	loaded, err := readMetaJSON(dir)
	if err != nil {
		t.Fatalf("readMetaJSON: %v", err)
	}
	if loaded.Name != meta.Name {
		t.Fatalf("Name: got %q, want %q", loaded.Name, meta.Name)
	}
}

// TestWriteMetaJSONProducesCorrectFile verifies that writeMetaJSON produces
// a correct, fully round-trippable meta.json. This test passes before and after
// the fix, proving no regression.
func TestWriteMetaJSONProducesCorrectFile(t *testing.T) {
	dir := t.TempDir()

	now := time.Now().Truncate(time.Millisecond)
	meta := sessionMeta{
		Name:         "roundtrip-test",
		Model:        "round-model",
		Provider:     "round-provider",
		CreatedAt:    now,
		UpdatedAt:    now.Add(time.Hour),
		TurnCount:    3,
		TokenCount:   100,
		ChunkCount:   2,
		MessageCount: 50,
		Dir:          dir,
		Worktree:     "feature-branch",
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		t.Fatalf("writeMetaJSON: %v", err)
	}

	loaded, err := readMetaJSON(dir)
	if err != nil {
		t.Fatalf("readMetaJSON: %v", err)
	}

	if loaded.Name != meta.Name {
		t.Errorf("Name: got %q, want %q", loaded.Name, meta.Name)
	}
	if loaded.Model != meta.Model {
		t.Errorf("Model: got %q, want %q", loaded.Model, meta.Model)
	}
	if loaded.Provider != meta.Provider {
		t.Errorf("Provider: got %q, want %q", loaded.Provider, meta.Provider)
	}
	if !loaded.CreatedAt.Equal(meta.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", loaded.CreatedAt, meta.CreatedAt)
	}
	if !loaded.UpdatedAt.Equal(meta.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v, want %v", loaded.UpdatedAt, meta.UpdatedAt)
	}
	if loaded.TurnCount != meta.TurnCount {
		t.Errorf("TurnCount: got %d, want %d", loaded.TurnCount, meta.TurnCount)
	}
	if loaded.TokenCount != meta.TokenCount {
		t.Errorf("TokenCount: got %d, want %d", loaded.TokenCount, meta.TokenCount)
	}
	if loaded.ChunkCount != meta.ChunkCount {
		t.Errorf("ChunkCount: got %d, want %d", loaded.ChunkCount, meta.ChunkCount)
	}
	if loaded.MessageCount != meta.MessageCount {
		t.Errorf("MessageCount: got %d, want %d", loaded.MessageCount, meta.MessageCount)
	}
	if loaded.Dir != meta.Dir {
		t.Errorf("Dir: got %q, want %q", loaded.Dir, meta.Dir)
	}
	if loaded.Worktree != meta.Worktree {
		t.Errorf("Worktree: got %q, want %q", loaded.Worktree, meta.Worktree)
	}
	if loaded.ToolAdmission != nil {
		t.Errorf("ToolAdmission: got %v, want nil", loaded.ToolAdmission)
	}
}

// TestWriteMetaJSONSyncErrorCleansTempFile verifies that a Sync error causes
// writeMetaJSON to clean up the temp file and return the error without
// writing meta.json (fail-closed, no partial state visible).
func TestWriteMetaJSONSyncErrorCleansTempFile(t *testing.T) {
	dir := t.TempDir()

	injectedErr := errors.New("simulated sync failure")
	original := syncFile
	syncFile = func(f *os.File) error {
		return injectedErr
	}
	t.Cleanup(func() { syncFile = original })

	meta := sessionMeta{
		Name:         "sync-err-test",
		Model:        "m",
		Provider:     "p",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		TurnCount:    1,
		TokenCount:   10,
		ChunkCount:   1,
		MessageCount: 2,
	}

	err := writeMetaJSON(dir, meta)
	if err == nil {
		t.Fatal("expected error when Sync fails, got nil")
	}
	if !errors.Is(err, injectedErr) {
		t.Fatalf("expected injected sync error, got: %v", err)
	}

	// meta.json must NOT exist on disk.
	metaPath := filepath.Join(dir, metaFileName)
	if _, statErr := os.Stat(metaPath); statErr == nil {
		t.Fatal("meta.json exists after Sync error; partial state visible on disk")
	}

	// No .meta-*.tmp files should remain.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) > 6 && e.Name()[:6] == ".meta-" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestWriteMetaJSONEmptyDir verifies that passing an empty directory returns
// an error (negative path).
func TestWriteMetaJSONEmptyDir(t *testing.T) {
	meta := sessionMeta{
		Name:      "empty-dir-test",
		Model:     "m",
		Provider:  "p",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := writeMetaJSON("", meta)
	if err == nil {
		t.Fatal("expected error for empty dir, got nil")
	}
}
