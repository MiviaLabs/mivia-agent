package chatsync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// identityKeyLen is the length of a derived identity key in hex characters.
// 128 bits of the digest is far more than a per-workspace file name needs and
// short enough to stay readable in a directory listing.
const identityKeyLen = 32

// SyncIdentity is the durable sync identity of ONE chat session. It is the
// record that makes cross-run resume work, and it is deliberately separate
// from the chat session's own principal.
//
// Three identities exist on this path and conflating any two of them is a
// defect:
//
//  1. LocalHandle - local only. It names the outbox directory and is the
//     cursor key. It never reaches the wire.
//  2. RemoteSessionID - the server-assigned id, the ONLY one that goes in a
//     URL. Persisting it is what lets a restart re-attach to the SAME remote
//     session instead of 404ing into a fresh one at lastSeq 0 while the local
//     cursor survives - the permanent sequence-gap 400.
//  3. The chat session id - a live capability (the contextstate authorization
//     subject). It appears NOWHERE in this struct, nowhere in a request body,
//     and nowhere in a URL. It is used only as the input to IdentityKey and as
//     the local event-bus filter.
type SyncIdentity struct {
	LocalHandle     string `json:"local_handle"`
	RemoteSessionID string `json:"remote_session_id,omitempty"`
	WriterID        string `json:"writer_id"`
}

// IdentityKey derives the lookup key for a chat session's sync identity file.
//
// The key must be stable across runs, or a restart cannot find the identity it
// wrote and every resume mints a new handle, a new outbox and a new remote
// session. The chat session id is the only session-scoped value with that
// property: chat.Session.Load retargets it at the resumed session
// (internal/chat/context_catalog.go reclaimContextSession, pinned by
// TestResumeAcrossProcessesUpdatesSameSessionInPlace), and /clear and an agent
// switch both keep it (internal/chat/session_id_stability_test.go). The
// session store directory is NOT usable: it is workspace-scoped and is copied
// between sessions by the TUI pool, so keying on it would merge unrelated
// conversations into one transcript.
//
// The chat session id is also the principal, so it is never stored. A SHA-256
// digest is stable exactly when the id is, is one-way, and grants nothing.
// This mirrors provider.sessionUserKey, which hashes the same value for the
// same reason.
//
// A session whose id is NOT restored on load (a file-backed session with no
// context store, whose meta.json carries no session id) simply gets a fresh
// key and a fresh identity, which is the correct answer: nothing else about
// that session is rebound to its previous run either.
func IdentityKey(chatSessionID string) string {
	sum := sha256.Sum256([]byte(chatSessionID))
	return fmt.Sprintf("%x", sum)[:identityKeyLen]
}

// newIdentityToken mints an unguessable local value. chatsync is a leaf
// package (settled decision 7) so it mints its own rather than importing
// runtime for one call.
func newIdentityToken() string {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(token[:])
}

// identityPath resolves the identity file for key inside dir. A key that is
// not a derived key is refused rather than sanitized: it names a file, and
// every caller gets its key from IdentityKey.
func identityPath(dir, key string) (string, error) {
	if len(key) != identityKeyLen {
		return "", fmt.Errorf("chatsync: identity key %q is not a derived key", key)
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("chatsync: identity key %q is not a derived key", key)
		}
	}
	return filepath.Join(dir, key+".json"), nil
}

// LoadOrCreateIdentity returns the persisted sync identity for key, minting
// and storing a fresh one when none is readable.
//
// A missing, unreadable, corrupt or incomplete file mints fresh rather than
// failing: losing the mapping costs one orphaned remote session, while
// refusing to start costs the user their sync entirely. The returned identity
// is always usable even when the error is non-nil - the error then reports
// only that the mint could not be made durable, so this run holds a handle
// the next run will not find.
func LoadOrCreateIdentity(dir, key string) (SyncIdentity, error) {
	path, err := identityPath(dir, key)
	if err != nil {
		return SyncIdentity{}, err
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		var id SyncIdentity
		if json.Unmarshal(data, &id) == nil && id.LocalHandle != "" && id.WriterID != "" {
			return id, nil
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		// A read error that is not "absent" (a permission problem, a
		// directory in the file's place) is reported, but a fresh identity is
		// still minted so sync runs.
		minted := SyncIdentity{LocalHandle: newIdentityToken(), WriterID: newIdentityToken()}
		return minted, fmt.Errorf("read sync identity: %w", readErr)
	}

	minted := SyncIdentity{LocalHandle: newIdentityToken(), WriterID: newIdentityToken()}
	if err := SaveIdentity(dir, key, minted); err != nil {
		return minted, err
	}
	return minted, nil
}

// SaveIdentity writes id durably: temp file, fsync, rename, matching the
// cursor's write discipline. A half-written identity file is indistinguishable
// from a corrupt one, and that costs a fresh remote session.
func SaveIdentity(dir, key string, id SyncIdentity) error {
	path, err := identityPath(dir, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create identity dir: %w", err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshal sync identity: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp sync identity: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write tmp sync identity: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync tmp sync identity: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp sync identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename sync identity: %w", err)
	}
	return nil
}
