package storage

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// readCatalogSessionID returns the stored chat_sessions.session_id for name,
// empty when the column is NULL.
func readCatalogSessionID(t *testing.T, store *SQLite, principal contextstate.Principal, name string) string {
	t.Helper()
	var sessionID sql.NullString
	if err := store.db.QueryRow(`SELECT session_id FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND name=?`, principal.WorkspaceID, principal.SubjectID, name).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	return sessionID.String
}

// TestSaveSessionStampsProjectionSessionID pins the "id is id, name is name"
// write rule: SaveSession persists chat_sessions.session_id only when the
// saving process declares a projection (opts.SessionID == name) AND a live
// context_sessions row backs it at write time. Every other shape stays NULL.
func TestSaveSessionStampsProjectionSessionID(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-session-123", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	if err := store.EnsureSession(ctx, contextstate.EnsureSessionRequest{Principal: principal, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`[{"role":"user","content":"hi"}]`)

	// (a) opts.SessionID == name with a live row: stamped with the live id.
	if err := store.SaveSession(ctx, principal, principal.SessionID, payload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogSessionID(t, store, principal, principal.SessionID); got != principal.SessionID {
		t.Fatalf("stamped session_id = %q, want the live id %q", got, principal.SessionID)
	}

	// (b) opts.SessionID == name but no live row: NULL.
	if err := store.SaveSession(ctx, principal, "no-live", payload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: "no-live"}); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogSessionID(t, store, principal, "no-live"); got != "" {
		t.Fatalf("session_id = %q for a missing live row, want empty", got)
	}

	// (c) opts.SessionID != name: NULL even though the live row exists.
	if err := store.SaveSession(ctx, principal, "copy", payload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogSessionID(t, store, principal, "copy"); got != "" {
		t.Fatalf("session_id = %q when opts.SessionID != name, want empty", got)
	}

	// (d) WorktreeInstance non-zero: always NULL, regardless of opts.SessionID.
	// The save needs an active managed worktree to be admitted.
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := store.BeginWorktreeCreation(ctx, principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(ctx, principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, principal, "wt-snap", payload, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance, SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	var wtSessionID sql.NullString
	if err := store.db.QueryRow(`SELECT session_id FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&wtSessionID); err != nil {
		t.Fatal(err)
	}
	if wtSessionID.Valid && wtSessionID.String != "" {
		t.Fatalf("worktree session_id = %q, want empty", wtSessionID.String)
	}

	// (e) Re-save (upsert) of a stamped row, still declaring the projection,
	// keeps the stamp.
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"second"}]`), "model", "provider", 2, 2, 4, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogSessionID(t, store, principal, principal.SessionID); got != principal.SessionID {
		t.Fatalf("stamp after re-save = %q, want the live id %q", got, principal.SessionID)
	}
}

// TestLoadSessionProjectionResolvesLivePayload: a projection row whose live
// session has a completed checkpoint serves the live checkpoint payload ("id
// is id") with the live session id, never the lagging snapshot.
func TestLoadSessionProjectionResolvesLivePayload(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-1", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, store, principal, binding, "hello")
	snapshot := []byte(`[{"role":"user","content":"stale snapshot"}]`)
	if err := store.SaveSession(ctx, principal, principal.SessionID, snapshot, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}

	payload, info, err := store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want the live id %q", info.SessionID, principal.SessionID)
	}
	if !bytes.Contains(payload, []byte("hello")) {
		t.Fatalf("payload = %q, want the live checkpoint payload (contains the turn content), not the snapshot", payload)
	}
}

// TestLoadSessionProjectionWithoutCheckpointKeepsIdentity: a projection whose
// live session exists but has no completed checkpoint serves the empty live payload
// while preserving the live identity (id is id) and title, so the
// caller still recognizes a live session to take over.
func TestLoadSessionProjectionWithoutCheckpointKeepsIdentity(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-2", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	ensureContextSession(t, store, principal, binding)
	if err := store.SetSessionTitle(ctx, principal, principal.SessionID, "my title", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`[{"role":"user","content":"snap"}]`)
	if err := store.SaveSession(ctx, principal, principal.SessionID, snapshot, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}

	payload, info, err := store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(emptyContextPayload) {
		t.Fatalf("payload = %q, want empty live context payload %q (no completed checkpoint)", payload, emptyContextPayload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q (identity preserved)", info.SessionID, principal.SessionID)
	}
	if info.Title != "my title" {
		t.Fatalf("info.Title = %q, want the live title %q", info.Title, "my title")
	}
}

// TestLoadSessionProjectionAfterClearAndShrink: a session whose live checkpoint
// shrinks (e.g. after /clear followed by a smaller 1-message turn) serves the
// live checkpoint payload rather than a stale pre-clear snapshot with more messages.
func TestLoadSessionProjectionAfterClearAndShrink(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-shrink-1", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, store, principal, binding, "first message before clear")

	// Snapshot carries 3 messages (pre-clear)
	snapshot := []byte(`[{"role":"user","content":"msg1"},{"role":"assistant","content":"msg2"},{"role":"user","content":"msg3"}]`)
	if err := store.SaveSession(ctx, principal, principal.SessionID, snapshot, "model", "provider", 3, 3, 3, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}

	// Now clear the session (ClearActive)
	snap, err := store.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Advance(ctx, contextstate.AdvanceRequest{
		Principal:         principal,
		SessionID:         principal.SessionID,
		OperationID:       "op-clear-1",
		Expected:          snap.Revision,
		NewSession:        snap.Revision.Session + 1,
		NewDurable:        snap.Revision.Durable + 1,
		NewSourceSequence: snap.Revision.Source,
		ExpectedBinding:   snap.Binding,
		NewBinding:        snap.Binding,
		ClearActive:       true,
		Reason:            "clear",
	}); err != nil {
		t.Fatal(err)
	}

	// Immediately after /clear, LoadSession must serve empty payload, not the 3-message snapshot
	payload, info, err := store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(emptyContextPayload) {
		t.Fatalf("payload after clear = %q, want %q", payload, emptyContextPayload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}

	// Commit a new single-message turn post-clear (shorter than the 3-message pre-clear snapshot)
	commitPostClearSingleMessage(t, store, principal, binding, "post clear question")

	// Even though chat_sessions snapshot STILL carries 3 messages (simulating lagging catalog auto-save),
	// LoadSession must serve the post-clear checkpoint (1 message), NOT the 3-message snapshot!
	payload, info, err = store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("post clear question")) {
		t.Fatalf("payload = %q, want post-clear turn checkpoint payload, got stale snapshot", payload)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want %q", info.SessionID, principal.SessionID)
	}
}

func commitPostClearSingleMessage(t *testing.T, store *SQLite, principal contextstate.Principal, binding contextstate.BindingRevision, content string) {
	t.Helper()
	ctx := context.Background()
	snapAfterClear, err := store.Load(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	newTurnPayload, err := contextstate.MarshalCanonical([]map[string]string{
		{"role": "user", "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	sequence := snapAfterClear.Revision.Source + 1
	sourceID, err := contextstate.NewSourceID(principal.SessionID, sequence)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(sourceID, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := contextstate.NewCheckpointID(principal.SessionID, rng, "context-compact-v1", 1, binding.Model, "post-clear-turn")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := contextstate.CheckpointRecord{
		ID:              checkpointID,
		Revision:        contextstate.Revision{Session: snapAfterClear.Revision.Session + 1, Durable: snapAfterClear.Revision.Durable + 1, Source: sequence},
		Binding:         snapAfterClear.Binding,
		SourceRange:     rng,
		ActiveContext:   newTurnPayload,
		SummaryMetadata: []byte(`{"version":1}`),
		TurnID:          2,
	}
	event := contextstate.SourceEvent{ID: sourceID, Kind: "message", Role: "user", Provenance: "test", RedactionStatus: "metadata", Size: len(content)}
	req, err := contextstate.NewCommitRequest(principal, principal.SessionID, snapAfterClear.Revision, snapAfterClear.Binding, []contextstate.SourceEvent{event}, checkpoint, newTurnPayload, snapAfterClear.Binding, sequence)
	if err != nil {
		t.Fatal(err)
	}
	req.Fingerprint, err = contextstate.FingerprintCommitRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, req); err != nil {
		t.Fatal(err)
	}
}

// TestLoadSessionProjectionWithTombstonedLiveRowIsPlainCopy: once the live
// row behind a projection is tombstoned, the projection serves its snapshot
// with an empty session id - a plain copy, not a live session.
func TestLoadSessionProjectionWithTombstonedLiveRowIsPlainCopy(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-3", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	ensureContextSession(t, store, principal, binding)
	snapshot := []byte(`[{"role":"user","content":"snap"}]`)
	if err := store.SaveSession(ctx, principal, principal.SessionID, snapshot, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE context_sessions SET tombstoned=1 WHERE workspace_id=? AND session_id=?`, principal.WorkspaceID, principal.SessionID); err != nil {
		t.Fatal(err)
	}

	payload, info, err := store.LoadSession(ctx, principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(snapshot) {
		t.Fatalf("payload = %q, want the snapshot %q", payload, snapshot)
	}
	if info.SessionID != "" {
		t.Fatalf("info.SessionID = %q, want empty (tombstoned live row makes a plain copy)", info.SessionID)
	}
}

// TestLoadSessionNamedCopyNeverTakesOverLiveSession pins the COLLISION RULE:
// a user-named copy (session_id NULL) is served as-is with an empty session
// id even when a live session of the same name exists - never a shadow, never
// a takeover.
func TestLoadSessionNamedCopyNeverTakesOverLiveSession(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "foo", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	ensureContextSession(t, store, principal, binding)
	if err := store.SetSessionTitle(ctx, principal, principal.SessionID, "live title", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`[{"role":"user","content":"copy"}]`)
	if err := store.SaveSession(ctx, principal, "foo", snapshot, "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{}); err != nil {
		t.Fatal(err)
	}

	payload, info, err := store.LoadSession(ctx, principal, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(snapshot) {
		t.Fatalf("payload = %q, want the named copy %q (the live session must not take over)", payload, snapshot)
	}
	if info.SessionID != "" {
		t.Fatalf("info.SessionID = %q, want empty (a named copy is never a live session)", info.SessionID)
	}
	if info.Title != "" {
		t.Fatalf("info.Title = %q, want empty (the live title must not be adopted)", info.Title)
	}
}

// TestLoadSessionFallsBackToLiveRowWithoutSnapshot: with no chat_sessions row
// at all, a live session is served directly (the arm2/--session path).
func TestLoadSessionFallsBackToLiveRowWithoutSnapshot(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "live-4", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	commitFirstMessageCheckpoint(t, store, principal, binding, "hello")

	payload, info, err := store.LoadSession(context.Background(), principal, principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != principal.SessionID {
		t.Fatalf("info.SessionID = %q, want the live id %q", info.SessionID, principal.SessionID)
	}
	if !bytes.Contains(payload, []byte("hello")) {
		t.Fatalf("payload = %q, want the live checkpoint payload", payload)
	}
}

// TestListSessionsProjectionAndNamedCopyStayDistinct: after a projection save
// (opts.SessionID == name, live row exists) plus a user-named copy ('foo',
// empty opts), the listing has exactly one entry per distinct session_id: the
// projection carries the live id and title, and the copy lists untitled with
// an empty session id.
func TestListSessionsProjectionAndNamedCopyStayDistinct(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	principal, err := contextstate.NewPrincipal("workspace", "live-session-456", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding := contextTestBinding(t)
	ensureContextSession(t, store, principal, binding)
	if err := store.SetSessionTitle(ctx, principal, principal.SessionID, "projection title", contextstate.WorktreeInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, principal, principal.SessionID, []byte(`[{"role":"user","content":"hi"}]`), "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{SessionID: principal.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ctx, principal, "foo", []byte(`[{"role":"user","content":"copy"}]`), "model", "provider", 1, 1, 2, contextstate.SessionSaveOptions{}); err != nil {
		t.Fatal(err)
	}

	infos, err := store.ListSessions(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("listing = %d entries, want 2 (one per distinct session_id)", len(infos))
	}
	var projection, copy contextstate.SessionCatalogInfo
	for _, info := range infos {
		switch info.Name {
		case principal.SessionID:
			projection = info
		case "foo":
			copy = info
		default:
			t.Fatalf("unexpected listing entry %q", info.Name)
		}
	}
	if projection.SessionID != principal.SessionID {
		t.Fatalf("projection session_id = %q, want the live id %q", projection.SessionID, principal.SessionID)
	}
	if projection.Title != "projection title" {
		t.Fatalf("projection title = %q, want %q", projection.Title, "projection title")
	}
	if copy.SessionID != "" || copy.Title != "" {
		t.Fatalf("copy entry = {session_id:%q title:%q}, want both empty", copy.SessionID, copy.Title)
	}
}
