package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestListSessionsOrdersAcrossMixedTimestampLayouts pins the fix for
// ListSessions ranking a stale RFC3339Nano-timestamped snapshot above a
// genuinely newer live session whose recency comes from
// context_checkpoints.created_at (SQLite's CURRENT_TIMESTAMP layout, no
// 'T'/'Z' - see sqliteTimestampLayout). The removed `ORDER BY 7 DESC,1`
// SQL clause compared these two layouts as raw strings, and RFC3339's 'T'
// sorts above ' ', so any RFC3339 row always outranked a same-day
// SQLite-layout row regardless of actual time.
func TestListSessionsOrdersAcrossMixedTimestampLayouts(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, err := contextstate.NewPrincipal("workspace", "live-session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A named snapshot, stamped with an old RFC3339Nano timestamp.
	stale := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO chat_sessions(workspace_id,subject_id,name,model,provider,messages,created_at,updated_at,turn_count,token_count,message_count)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, "stale-snapshot", "m", "p", []byte(`[{}]`), stale, stale, 1, 1, 1); err != nil {
		t.Fatalf("seed stale snapshot: %v", err)
	}

	// A live session with one recent, complete checkpoint - the recency
	// this row's own layout (sqliteTimestampLayout) records.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO context_sessions(workspace_id,session_id,subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned)
		 VALUES(?,?,?,?,0,0,1,'p','m',0,0)`,
		principal.WorkspaceID, principal.SessionID, principal.SubjectID, principal.CapabilityDigest()); err != nil {
		t.Fatalf("seed live session: %v", err)
	}
	fresh := time.Now().UTC().Format(sqliteTimestampLayout)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete,created_at)
		 VALUES('ckpt-fresh',?,?,?,0,0,'a',1,'m','op-fresh','idem-fresh',0,0,0,0,x'00',x'00','fp',1,?)`,
		principal.WorkspaceID, principal.SessionID, principal.SubjectID, fresh); err != nil {
		t.Fatalf("seed fresh checkpoint: %v", err)
	}

	infos, err := store.ListSessions(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("ListSessions = %+v, want 2 rows", infos)
	}
	if infos[0].SessionID != principal.SessionID {
		t.Fatalf("ListSessions[0] = %+v, want the genuinely newer live session first", infos[0])
	}
	if infos[1].Name != "stale-snapshot" {
		t.Fatalf("ListSessions[1] = %+v, want the stale snapshot last", infos[1])
	}
}
