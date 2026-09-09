package chatsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIdentityPath_RejectsNonHexKey pins identityPath's per-rune guard,
// distinct from TestLoadOrCreateIdentityRejectsAKeyThatIsNotADerivedKey
// (which uses a wrong-length key and never reaches the hex loop): a
// correct-length key containing a non-hex rune must still be refused.
func TestIdentityPath_RejectsNonHexKey(t *testing.T) {
	notHex := strings.Repeat("g", identityKeyLen)
	if _, err := identityPath(t.TempDir(), notHex); err == nil {
		t.Fatal("identityPath accepted a correct-length key with a non-hex rune")
	}
}

// TestLoadOrCreateIdentity_NonEphemeralKeyErrorMintsAnyway pins the second
// identityPath error branch in LoadOrCreateIdentity: an invalid key that is
// NOT errNoIdentityDir must still surface the error (not silently mint), the
// counterpart to TestLoadOrCreateIdentityWithNoStoreDirStaysEphemeral above.
func TestLoadOrCreateIdentity_NonEphemeralKeyErrorMintsAnyway(t *testing.T) {
	if _, err := LoadOrCreateIdentity(t.TempDir(), "too-short"); err == nil {
		t.Fatal("LoadOrCreateIdentity accepted a key that is not a derived key")
	}
}

// TestSaveIdentity_ErrorChain drives every write-path guard in SaveIdentity
// in isolation, following the same read-only-directory and directory-in-
// place-of-a-file techniques already established for the outbox's durable
// writers.
func TestSaveIdentity_ErrorChain(t *testing.T) {
	key := IdentityKey("principal-save-errors")

	t.Run("bad key", func(t *testing.T) {
		if err := SaveIdentity(t.TempDir(), "not-a-key", SyncIdentity{}); err == nil {
			t.Fatal("SaveIdentity accepted a key that is not a derived key")
		}
	})

	t.Run("MkdirAll fails when parent is a file", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SaveIdentity(filepath.Join(blocker, "sub"), key, SyncIdentity{}); err == nil {
			t.Fatal("SaveIdentity accepted a dir path with a file for a parent")
		}
	})

	t.Run("tmp file create fails on a read-only dir", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		probe := filepath.Join(dir, "writability-probe")
		if f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_ = f.Close()
			_ = os.Remove(probe)
			t.Skip("platform still creates files in a read-only directory")
		}
		if err := SaveIdentity(dir, key, SyncIdentity{LocalHandle: "h"}); err == nil {
			t.Fatal("SaveIdentity accepted a directory it cannot write into")
		}
	})

	t.Run("rename fails when the target path is a directory", func(t *testing.T) {
		dir := t.TempDir()
		path, err := identityPath(dir, key)
		if err != nil {
			t.Fatal(err)
		}
		// A directory in place of the final target makes the rename's
		// destination a non-empty-dir-over-file conflict on every platform
		// this repo supports, without touching the tmp file's own creation.
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := SaveIdentity(dir, key, SyncIdentity{LocalHandle: "h"}); err == nil {
			t.Fatal("SaveIdentity accepted a target path that is a directory")
		}
	})
}

// TestLoadOrCreateIdentity_ReadFileNonNotExistErrorSurfaces pins the
// branch where os.ReadFile fails with something OTHER than ErrNotExist
// (a directory sitting where the identity file should be, which fails
// with "is a directory" rather than "not exist" on every platform this
// repo supports): LoadOrCreateIdentity must still mint a usable identity
// AND report the read error, rather than silently minting.
func TestLoadOrCreateIdentity_ReadFileNonNotExistErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("principal-read-error")
	path, err := identityPath(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	id, loadErr := LoadOrCreateIdentity(dir, key)
	if loadErr == nil {
		t.Fatal("LoadOrCreateIdentity hid a non-ErrNotExist read failure")
	}
	if id.LocalHandle == "" {
		t.Fatal("LoadOrCreateIdentity did not mint a usable identity despite the read error")
	}
}

// TestLoadOrCreateIdentity_MintSaveErrorSurfaces pins the branch where a
// fresh mint's own SaveIdentity fails: LoadOrCreateIdentity must still
// return the minted (usable-this-run) identity alongside the error.
func TestLoadOrCreateIdentity_MintSaveErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	key := IdentityKey("principal-mint-save-error")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	probe := filepath.Join(dir, "writability-probe")
	if f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		_ = os.Remove(probe)
		t.Skip("platform still creates files in a read-only directory")
	}
	id, err := LoadOrCreateIdentity(dir, key)
	if err == nil {
		t.Fatal("LoadOrCreateIdentity hid a mint-save failure")
	}
	if id.LocalHandle == "" {
		t.Fatal("LoadOrCreateIdentity did not return the usable minted identity despite the save error")
	}
}
