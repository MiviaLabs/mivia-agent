package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/context/state"
)

// TestTwoSessionsPersistIdenticalContent is the durable regression for the
// live `checkpoint conflict`. Two `mivia chat` runs share one workspace store,
// and both produce the same bytes. The second run's whole commit used to be
// rejected because context_payloads.ref was the bare content digest while the
// row's owner columns had to match the writer.
func TestTwoSessionsPersistIdenticalContent(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "shared.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	appendTurn := func(sessionID string) error {
		principal, err := state.NewPrincipal("workspace", sessionID, "local-user")
		if err != nil {
			t.Fatal(err)
		}
		seedContextSession(t, store, principal)
		payload, err := state.SanitizeSourcePayload(ctx, principal, []byte("hello"), state.RedactionPolicy{})
		if err != nil {
			t.Fatal(err)
		}
		id, err := state.NewSourceID(sessionID, 1)
		if err != nil {
			t.Fatal(err)
		}
		event := state.SourceEvent{
			ID: id, Kind: "message", Role: "user", PayloadRef: payload.Ref.Ref,
			Provenance: "host-turn", RedactionStatus: "metadata", Size: payload.Ref.Size,
		}
		record := state.PayloadRecord{Ref: payload.Ref, Retention: payload.Retention}
		return store.appendSourceEvents(ctx, principal, []state.SourceEvent{event}, []state.PayloadRecord{record})
	}

	if err := appendTurn("session-one"); err != nil {
		t.Fatalf("first session: %v", err)
	}
	if err := appendTurn("session-two"); err != nil {
		t.Fatalf("second session repeating identical content: %v", err)
	}

	var rows int
	if err := store.db.QueryRow(`SELECT count(*) FROM context_source_events`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("source events = %d, want one per session", rows)
	}
}

// TestLegacyPayloadRowStillResolves proves the fix needs no data migration:
// a row written under the old content-addressed key is still readable, because
// every read path looks a payload up by the reference string stored on its
// source event and never re-derives one from the content.
func TestLegacyPayloadRowStillResolves(t *testing.T) {
	ctx := context.Background()
	store, principal := openContextTestStore(t)
	defer store.Close()
	seedContextSession(t, store, principal)

	// "ctxp_" + sha256("legacy bytes") - the pre-fix reference shape.
	const legacyRef = "ctxp_5f4b8f1a8f5c9e7e6f47e2ad38a1d1c1a5c0a1d0f4bbf5e2a29bd7c6a53c1c95"
	const legacyDigest = "5f4b8f1a8f5c9e7e6f47e2ad38a1d1c1a5c0a1d0f4bbf5e2a29bd7c6a53c1c95"
	if _, err := store.db.Exec(
		`INSERT INTO context_payloads(ref,namespace,workspace_id,session_id,subject_id,sha256,size,redaction_status,retention_class,revoked,data) VALUES(?,?,?,?,?,?,?,?,?,0,NULL)`,
		legacyRef, state.Namespace, principal.WorkspaceID, principal.SessionID, principal.SubjectID,
		legacyDigest, 12, "metadata", string(state.RetentionSession),
	); err != nil {
		t.Fatal(err)
	}
	ref := state.ContentRef{
		Ref: legacyRef, Namespace: state.Namespace, SHA256: legacyDigest,
		WorkspaceID: principal.WorkspaceID, SessionID: principal.SessionID,
		SubjectID: principal.SubjectID, Size: 12,
	}
	got, err := store.ReadPayload(ctx, principal, ref)
	if err != nil {
		t.Fatalf("legacy payload no longer resolves: %v", err)
	}
	if got.Ref.Ref != legacyRef {
		t.Fatalf("resolved reference = %q, want the stored legacy key", got.Ref.Ref)
	}
}
