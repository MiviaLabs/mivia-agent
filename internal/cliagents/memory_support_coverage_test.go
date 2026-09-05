package cliagents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestSameFilePath(t *testing.T) {
	cases := []struct {
		goos, a, b string
		want       bool
	}{
		{"linux", "/tmp/mivia/m.db", "/tmp/mivia/m.db", true},
		{"windows", `C:\Temp\M.DB`, `c:\temp\m.db`, true},
		{"linux", "/tmp/M.DB", "/tmp/m.db", false},
		{"linux", "/tmp/x/../m.db", "/tmp/m.db", true},
		{"linux", "", "/tmp/m.db", false},
	}
	for _, tc := range cases {
		if got := SameFilePath(tc.goos, tc.a, tc.b); got != tc.want {
			t.Errorf("SameFilePath(%q,%q,%q)=%v, want %v", tc.goos, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestOpenMemoryStoreWithReadOnly_MemoryBackend covers the ephemeral
// memory.BackendMemory branch of OpenMemoryStoreWithReadOnly, which the
// package's other fixtures never exercise (they all use the Markdown
// backend). A successful Save/Close round trip proves the delegated
// memory.Open call actually wires a working store, not just a non-nil
// return.
func TestOpenMemoryStoreWithReadOnly_MemoryBackend(t *testing.T) {
	root := t.TempDir()
	store, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{
		StoreBackend: memory.BackendMemory, MaxEntryBytes: memory.DefaultMaxEntryBytes,
		MaxSearchResults: memory.DefaultMaxSearchResults,
	}, false)
	if err != nil {
		t.Fatalf("OpenMemoryStoreWithReadOnly(memory backend): %v", err)
	}
	defer store.Close()
	if _, err := store.Save(context.Background(), memory.Entry{
		Title: "In-memory fact", Scope: memory.ScopeProject, Verdict: memory.VerdictGood,
		Summary: "ephemeral", Why: "test",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestOpenMemoryStoreWithReadOnly_UnsupportedBackend covers the default
// case: a store_backend value that is neither "markdown" nor "memory" must
// fail closed rather than silently falling back to a default backend.
func TestOpenMemoryStoreWithReadOnly_UnsupportedBackend(t *testing.T) {
	root := t.TempDir()
	_, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{StoreBackend: "sqlite-direct"}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly: expected error for unsupported backend, got nil")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want mention of unsupported backend", err)
	}
}

// TestOpenMemoryStoreWithReadOnly_MissingHomeWithOrgID covers
// openMarkdownMemoryStore's guard against an unresolvable global memory
// directory: an empty HOME makes workspace.GlobalMemoryDir return "", which
// must fail closed whenever an OrgID is configured (org memory would
// otherwise silently resolve under a wrong/empty path).
func TestOpenMemoryStoreWithReadOnly_MissingHomeWithOrgID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", "")
	_, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{
		StoreBackend: memory.BackendMarkdown, OrgID: "acme",
	}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly: expected error when org memory dir is unavailable, got nil")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("error = %v, want mention of unavailable org dir", err)
	}
}

// TestOpenMemoryStoreWithReadOnly_InvalidOrgID covers the propagated
// memory.NewMarkdownSource error path: with a resolvable HOME (so the
// missing-org-dir guard above does not fire first), a syntactically invalid
// OrgID (containing "..") must still surface as a wrapped "memory Markdown
// source" error.
func TestOpenMemoryStoreWithReadOnly_InvalidOrgID(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	_, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{
		StoreBackend: memory.BackendMarkdown, OrgID: "../escape",
	}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly: expected error for invalid org id, got nil")
	}
	if !strings.Contains(err.Error(), "Markdown source") {
		t.Fatalf("error = %v, want wrapped Markdown source error", err)
	}
}

// TestOpenMemoryStoreWithReadOnly_IndexOpenFails covers the storage.
// OpenSQLiteWithOptions error path: pointing HOME at a regular file (not a
// directory) makes the derived global context-index path's parent
// uncreatable, so opening the index must fail and the error must surface
// wrapped as "memory index".
func TestOpenMemoryStoreWithReadOnly_IndexOpenFails(t *testing.T) {
	root := t.TempDir()
	homeFile := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(homeFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed homeFile: %v", err)
	}
	t.Setenv("HOME", homeFile)
	_, err := OpenMemoryStoreWithReadOnly(root, config.MemoryConfig{StoreBackend: memory.BackendMarkdown}, false)
	if err == nil {
		t.Fatal("OpenMemoryStoreWithReadOnly: expected error opening memory index under a non-directory HOME, got nil")
	}
	if !strings.Contains(err.Error(), "memory index") {
		t.Fatalf("error = %v, want wrapped memory index error", err)
	}
}

// erroringMemoryStore is a minimal memory.Store stub whose only purpose is
// making Close fail on demand; every other method is unused by
// TestOwnedMarkdownStore_CloseReturnsStoreCloseError below.
type erroringMemoryStore struct {
	memory.Store
	closeErr error
}

func (e erroringMemoryStore) Close() error { return e.closeErr }

// TestOwnedMarkdownStore_CloseReturnsStoreCloseError covers
// ownedMarkdownStore.Close's storeErr-takes-priority branch: when the
// wrapped memory.Store fails to close, Close must still close the index
// (proven by the index being usable afterward, since a leaked SQLite
// connection is the only thing that would break TempDir cleanup) but must
// report the store's error, not swallow it in favor of a (here nil) index
// error.
func TestOwnedMarkdownStore_CloseReturnsStoreCloseError(t *testing.T) {
	index, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	wantErr := errors.New("store close boom")
	s := &ownedMarkdownStore{Store: erroringMemoryStore{closeErr: wantErr}, index: index}
	if err := s.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("Close() = %v, want %v", err, wantErr)
	}
}
