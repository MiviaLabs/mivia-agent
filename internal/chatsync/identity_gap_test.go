package chatsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadIdentityReadOnlyWithNoIdentityDir(t *testing.T) {
	if _, ok := LoadIdentityReadOnly("", IdentityKey("sess-1")); ok {
		t.Fatal("LoadIdentityReadOnly with an empty dir reported ok=true")
	}
}

func TestLoadIdentityReadOnlyRejectsMalformedOrIncompleteFile(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("sess-1")
	path := filepath.Join(dir, key+".json")

	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadIdentityReadOnly(dir, key); ok {
		t.Fatal("LoadIdentityReadOnly accepted malformed JSON")
	}

	if err := os.WriteFile(path, []byte(`{"remote_session_id":"r1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadIdentityReadOnly(dir, key); ok {
		t.Fatal("LoadIdentityReadOnly accepted an identity with no local_handle")
	}
}
