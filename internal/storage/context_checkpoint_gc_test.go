package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// checkpointGCFixture seeds one session with n complete checkpoints, all aged
// past any retention window, and returns the store plus the session's
// identifiers. Rows are inserted directly: this test is about the retention
// predicate, not about the commit path that normally produces them.
func checkpointGCFixture(t *testing.T, n int) (*SQLite, contextstate.Principal, string) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "sess-gc"
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO context_sessions(workspace_id,session_id,subject_id,capability_digest,session_revision,durable_revision,source_sequence,provider,model,binding_generation,tombstoned)
		 VALUES(?,?,?,?,0,0,0,'p','m',0,0)`,
		principal.WorkspaceID, sessionID, principal.SubjectID, principal.CapabilityDigest()); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	old := time.Now().UTC().Add(-90 * 24 * time.Hour)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ckpt-%03d", i)
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO context_checkpoints(checkpoint_id,workspace_id,session_id,subject_id,source_start,source_end,algorithm,schema_version,summary_model,operation_id,idempotency_key,session_revision,durable_revision,binding_generation,turn_id,summary_metadata,active_context,content_fingerprint,complete,created_at)
			 VALUES(?,?,?,?,?,?,'a',1,'m',?,?,0,0,0,?,x'00',x'00','fp',1,?)`,
			id, principal.WorkspaceID, sessionID, principal.SubjectID, i, i,
			"op-"+id, "idem-"+id, i, old.Format(sqliteTimestampLayout)); err != nil {
			t.Fatalf("seed checkpoint %d: %v", i, err)
		}
	}
	return store, principal, sessionID
}

func checkpointIDs(t *testing.T, store *SQLite) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT checkpoint_id FROM context_checkpoints ORDER BY checkpoint_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestPruneSessionCheckpointsKeepsFloorAndEarliest is the core contract: aged
// checkpoints go, but the earliest complete one (the first-message source) and
// the newest keep rows stay, so no reachable read loses its row.
func TestPruneSessionCheckpointsKeepsFloorAndEarliest(t *testing.T) {
	store, _, _ := checkpointGCFixture(t, 10)
	removed, err := store.PruneSessionCheckpoints(context.Background(), time.Now().UTC(), 30*24*time.Hour, 2, 1000)
	if err != nil {
		t.Fatalf("PruneSessionCheckpoints: %v", err)
	}
	// 10 rows, minus the earliest, minus the newest 2 = 7 candidates.
	if removed != 7 {
		t.Fatalf("removed = %d, want 7", removed)
	}
	got := checkpointIDs(t, store)
	want := []string{"ckpt-000", "ckpt-008", "ckpt-009"}
	if len(got) != len(want) {
		t.Fatalf("survivors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("survivors = %v, want %v", got, want)
		}
	}
}

// TestPruneSessionCheckpointsSpsActiveCheckpoint proves the active checkpoint
// is never a candidate even when it is old and outside the keep floor.
func TestPruneSessionCheckpointsSparesActiveCheckpoint(t *testing.T) {
	store, principal, sessionID := checkpointGCFixture(t, 10)
	ctx := context.Background()
	// ckpt-003 is aged, not earliest, and not in the newest 2 - it would be a
	// candidate if it were not the session's active checkpoint.
	if _, err := store.db.ExecContext(ctx, `UPDATE context_sessions SET source_sequence=9, active_checkpoint_id='ckpt-003' WHERE workspace_id=? AND session_id=? AND subject_id=?`,
		principal.WorkspaceID, sessionID, principal.SubjectID); err != nil {
		t.Fatalf("set active checkpoint: %v", err)
	}
	if _, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), 30*24*time.Hour, 2, 1000); err != nil {
		t.Fatalf("PruneSessionCheckpoints: %v", err)
	}
	for _, id := range checkpointIDs(t, store) {
		if id == "ckpt-003" {
			return
		}
	}
	t.Fatal("active checkpoint ckpt-003 was pruned")
}

// TestPruneSessionCheckpointsSparesIncomplete keeps in-flight checkpoints,
// which the recovery path reads to resume an interrupted commit.
func TestPruneSessionCheckpointsSparesIncomplete(t *testing.T) {
	store, _, _ := checkpointGCFixture(t, 10)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE context_checkpoints SET complete=0 WHERE checkpoint_id='ckpt-004'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), 30*24*time.Hour, 2, 1000); err != nil {
		t.Fatalf("PruneSessionCheckpoints: %v", err)
	}
	for _, id := range checkpointIDs(t, store) {
		if id == "ckpt-004" {
			return
		}
	}
	t.Fatal("incomplete checkpoint ckpt-004 was pruned")
}

// TestPruneSessionCheckpointsHonoursRetentionWindow proves recent rows survive:
// a young store must lose nothing at all.
func TestPruneSessionCheckpointsHonoursRetentionWindow(t *testing.T) {
	store, _, _ := checkpointGCFixture(t, 10)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE context_checkpoints SET created_at=?`, time.Now().UTC().Format(sqliteTimestampLayout)); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), 30*24*time.Hour, 2, 1000)
	if err != nil {
		t.Fatalf("PruneSessionCheckpoints: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d inside the retention window, want 0", removed)
	}
}

// TestPruneSessionCheckpointsIsIdempotent: a second sweep is a no-op, so the
// operation is safe to re-run (ADLC step-6 idempotency).
func TestPruneSessionCheckpointsIsIdempotent(t *testing.T) {
	store, _, _ := checkpointGCFixture(t, 10)
	ctx := context.Background()
	if _, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), 30*24*time.Hour, 2, 1000); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), 30*24*time.Hour, 2, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("second sweep removed %d rows, want 0", removed)
	}
}

// TestPruneSessionCheckpointsRejectsBadBounds keeps the guard fail-closed.
func TestPruneSessionCheckpointsRejectsBadBounds(t *testing.T) {
	store, _, _ := checkpointGCFixture(t, 3)
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		retention time.Duration
		keep      int
		limit     int
	}{
		{"negative retention", -time.Hour, 2, 10},
		{"negative keep", time.Hour, -1, 10},
		{"zero limit", time.Hour, 2, 0},
		{"oversized limit", time.Hour, 2, 10_001},
	} {
		if _, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), tc.retention, tc.keep, tc.limit); err == nil {
			t.Fatalf("%s: expected rejection", tc.name)
		}
	}
	if got := len(checkpointIDs(t, store)); got != 3 {
		t.Fatalf("rejected sweeps changed rows: %d left, want 3", got)
	}
}
