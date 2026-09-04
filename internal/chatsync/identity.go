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

// LocalHandle is the local-only sync handle: the outbox directory name and
// the cursor key. It is a distinct type so a chat principal cannot be assigned
// into it by accident.
//
// Be honest about what this type does and does not buy. It is NOT a barrier:
// LocalHandle(sess.SessionID) compiles. Two things enforce the separation, and
// the type only makes them easier to see:
//
//   - mivia.go.no-chat-principal-as-sync-handle rejects the conversion form.
//   - TestChatPrincipalNeverReachesTheWireOrDisk proves at runtime that the
//     principal is in no request target, no request body and no file this
//     package writes. That test is the real gate; it survives every loophole
//     the type and the rule miss.
type LocalHandle string

// IdentityDir is where the sync identity records for a store directory live.
// One file per session key - REVIEW CHANGE 7 - so two processes sharing a
// store directory never read-modify-write one shared file.
// An empty store dir yields an empty identity dir, not a RELATIVE one:
// filepath.Join("", "chat-sync", "identity") is "chat-sync/identity", which
// would put a durable record wherever the process happens to be running.
func IdentityDir(storeDir string) string {
	if storeDir == "" {
		return ""
	}
	return filepath.Join(storeDir, "chat-sync", "identity")
}

// OutboxDirFor is the outbox directory for a local handle. The layout lives
// here rather than in each host because two hosts (the plain CLI surface and
// the TUI session pool) build it, and a layout that drifts between them
// silently orphans one surface's cursor.
//
// An empty store dir yields an empty outbox dir, not a RELATIVE one - same
// reasoning as IdentityDir above. Without this guard, a caller that failed to
// resolve a real store dir silently got an outbox at "chat-sync/sessions/<handle>"
// relative to wherever the process's cwd happened to be, which for `mivia chat`
// run from inside a project checkout means real conversation transcripts land
// inside the project tree itself. OpenOutbox refuses an empty dir (MkdirAll
// fails), so the caller's sync attach fails closed instead.
func OutboxDirFor(storeDir string, handle LocalHandle) string {
	if storeDir == "" {
		return ""
	}
	return filepath.Join(storeDir, "chat-sync", "sessions", string(handle))
}

// IdentityRef locates a stored identity so a running session can write back
// the remote session id it resolved. A zero IdentityRef disables the
// write-back, which is what every test that does not care about resume uses.
type IdentityRef struct {
	Dir string
	Key string
}

// IsZero reports whether the ref names no identity file.
func (r IdentityRef) IsZero() bool { return r.Dir == "" || r.Key == "" }

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
	LocalHandle     LocalHandle `json:"local_handle"`
	RemoteSessionID string      `json:"remote_session_id,omitempty"`
	WriterID        string      `json:"writer_id"`
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

// mintIdentity returns a fresh, unused identity. The handle and the writer id
// are independent draws: the writer id travels in every event payload, so
// deriving one from the other would put a local-only value on the wire.
func mintIdentity() SyncIdentity {
	return SyncIdentity{
		LocalHandle: LocalHandle(newIdentityToken()),
		WriterID:    newIdentityToken(),
	}
}

// identityPath resolves the identity file for key inside dir. A key that is
// not a derived key is refused rather than sanitized: it names a file, and
// every caller gets its key from IdentityKey.
func identityPath(dir, key string) (string, error) {
	if dir == "" {
		return "", errNoIdentityDir
	}
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

// errNoIdentityDir reports that there is nowhere durable to keep an identity.
// A session with no store directory must not write one into the process's
// working directory, so it runs on an ephemeral identity instead.
var errNoIdentityDir = errors.New("chatsync: no identity directory")

// LoadIdentityReadOnly reads the persisted sync identity for key WITHOUT
// minting or creating anything on disk. If the file is missing, invalid, or
// unreadable, it returns (SyncIdentity{}, false).
func LoadIdentityReadOnly(dir, key string) (SyncIdentity, bool) {
	path, err := identityPath(dir, key)
	if err != nil {
		return SyncIdentity{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SyncIdentity{}, false
	}
	var id SyncIdentity
	if err := json.Unmarshal(data, &id); err != nil || id.LocalHandle == "" {
		return SyncIdentity{}, false
	}
	return id, true
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
	if errors.Is(err, errNoIdentityDir) {
		// Ephemeral: usable this run, unfindable next run. The caller is a
		// session with no store directory, which has no cross-run state of any
		// other kind either.
		return mintIdentity(), err
	}
	if err != nil {
		return SyncIdentity{}, err
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		var id SyncIdentity
		if json.Unmarshal(data, &id) == nil && id.LocalHandle != "" {
			if id.WriterID == "" {
				// Backfill rather than re-mint. A record written before the
				// writer id was wired is still the right OUTBOX and the right
				// remote session; discarding it over a missing field would
				// orphan both. Best effort: an unwritable backfill costs the
				// next run a new writer id, nothing else.
				id.WriterID = newIdentityToken()
				_ = SaveIdentity(dir, key, id)
			}
			return id, nil
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		// A read error that is not "absent" (a permission problem, a
		// directory in the file's place) is reported, but a fresh identity is
		// still minted so sync runs.
		return mintIdentity(), fmt.Errorf("read sync identity: %w", readErr)
	}

	minted := mintIdentity()
	if err := SaveIdentity(dir, key, minted); err != nil {
		return minted, err
	}
	return minted, nil
}

// persistRemoteSessionID records the remote session this run attached to, so
// the next run re-attaches to it instead of creating a new one. It is a no-op
// when the options name no identity file.
//
// The record is rebuilt from the options rather than re-read from disk: a
// re-read of a file that vanished mid-run would mint a DIFFERENT handle and
// store it, leaving the identity naming an outbox this process is not using.
func (o SessionOptions) persistRemoteSessionID(remoteSessionID string) error {
	if o.Identity.IsZero() || remoteSessionID == "" {
		return nil
	}
	return SaveIdentity(o.Identity.Dir, o.Identity.Key, SyncIdentity{
		LocalHandle:     o.LocalHandle,
		RemoteSessionID: remoteSessionID,
		WriterID:        o.ProjectorOptions.WriterID,
	})
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
