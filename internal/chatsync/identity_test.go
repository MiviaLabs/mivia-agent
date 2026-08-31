package chatsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sync identity is the file that makes cross-run resume possible: it holds
// the local handle (the outbox directory name and cursor key), the remote
// session id, and the writer id. It is looked up by a key derived from the
// chat session id, never by the chat session id itself.

func TestIdentityKeyIsDerivedNotThePrincipal(t *testing.T) {
	const principal = "AAAABBBBCCCCDDDDEEEEFFFFGG"

	key := IdentityKey(principal)

	if key == principal {
		t.Fatalf("IdentityKey returned the principal verbatim: %q", key)
	}
	if strings.Contains(key, principal) {
		t.Fatalf("IdentityKey embedded the principal: %q", key)
	}
	if got := IdentityKey(principal); got != key {
		t.Fatalf("IdentityKey is not deterministic: %q then %q", key, got)
	}
	if IdentityKey(principal+"x") == key {
		t.Fatal("IdentityKey collided for two different session ids")
	}
	if len(key) != 32 {
		t.Fatalf("IdentityKey length = %d, want 32", len(key))
	}
	for _, r := range key {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("IdentityKey %q is not lowercase hex; it names a file", key)
		}
	}
}

func TestLoadOrCreateIdentityMintsThenReuses(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("principal-1")

	first, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	if first.LocalHandle == "" || first.WriterID == "" {
		t.Fatalf("minted identity is incomplete: %+v", first)
	}
	if string(first.LocalHandle) == key || string(first.LocalHandle) == "principal-1" {
		t.Fatalf("local handle must be freshly minted, got %q", first.LocalHandle)
	}
	if first.WriterID == string(first.LocalHandle) {
		t.Fatal("writer id and local handle must be independent values")
	}

	second, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity (second run): %v", err)
	}
	if second.LocalHandle != first.LocalHandle || second.WriterID != first.WriterID {
		t.Fatalf("identity is not stable across runs: %+v then %+v", first, second)
	}
}

func TestSaveIdentityRoundTripsTheRemoteSessionID(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("principal-2")

	id, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	id.RemoteSessionID = "remote-abc"
	if err := SaveIdentity(dir, key, id); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	reloaded, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity after save: %v", err)
	}
	if reloaded.RemoteSessionID != "remote-abc" {
		t.Fatalf("RemoteSessionID = %q, want remote-abc; without it every restart 404s into a fresh session", reloaded.RemoteSessionID)
	}
	if reloaded.LocalHandle != id.LocalHandle {
		t.Fatalf("LocalHandle changed across save: %q then %q", id.LocalHandle, reloaded.LocalHandle)
	}
}

func TestLoadOrCreateIdentityMintsOverACorruptFile(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("principal-3")

	first, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}

	path := filepath.Join(dir, key+".json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the identity file: %v", err)
	}

	second, err := LoadOrCreateIdentity(dir, key)
	if err != nil {
		t.Fatalf("a corrupt identity file must mint fresh, not fail: %v", err)
	}
	if second.LocalHandle == "" || second.WriterID == "" {
		t.Fatalf("identity minted over a corrupt file is incomplete: %+v", second)
	}
	if second.LocalHandle == first.LocalHandle {
		t.Fatal("the corrupt file was not actually replaced; this test proves nothing")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten identity: %v", err)
	}
	var onDisk SyncIdentity
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("the rewritten identity file is not valid JSON: %v", err)
	}
	if onDisk.LocalHandle != second.LocalHandle {
		t.Fatalf("on-disk handle %q does not match the returned %q", onDisk.LocalHandle, second.LocalHandle)
	}
}

func TestLoadOrCreateIdentityRejectsAKeyThatIsNotADerivedKey(t *testing.T) {
	dir := t.TempDir()

	if _, err := LoadOrCreateIdentity(dir, "../escape"); err == nil {
		t.Fatal("a key that is not a derived key must be refused; it names a file")
	}
	if _, err := LoadOrCreateIdentity(dir, ""); err == nil {
		t.Fatal("an empty key must be refused")
	}
}
