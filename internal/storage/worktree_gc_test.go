package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// worktreeGCFixture seeds one instance per lifecycle state, all aged well past
// any retention window, plus a route for each.
func worktreeGCFixture(t *testing.T) (*SQLite, contextstate.Principal) {
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
	old := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	ctx := context.Background()
	for _, state := range []contextstate.WorktreeInstanceState{
		contextstate.WorktreeCreating,
		contextstate.WorktreeActive,
		contextstate.WorktreeDeleting,
		contextstate.WorktreeDeleted,
	} {
		id := "wt_" + string(state)
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO worktree_instances(workspace_id,worktree,instance_id,canonical_path,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			principal.WorkspaceID, string(state), id, "/tmp/"+id, string(state), old, old); err != nil {
			t.Fatalf("seed instance %s: %v", state, err)
		}
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?)`,
			principal.WorkspaceID, principal.SubjectID, string(state), "/tmp/"+id, old, old, id); err != nil {
			t.Fatalf("seed route %s: %v", state, err)
		}
	}
	return store, principal
}

func instanceStates(t *testing.T, store *SQLite) map[string]bool {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT state FROM worktree_instances`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out[s] = true
	}
	return out
}

func routeCount(t *testing.T, store *SQLite) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM worktree_routes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPruneWorktreeInstancesReapsTerminalAndAbandoned is the core contract:
// 'deleted' (terminal) and 'creating' (abandoned mid-create) go; 'active' and
// 'deleting' never do, because either may still describe a live worktree.
func TestPruneWorktreeInstancesReapsTerminalAndAbandoned(t *testing.T) {
	store, _ := worktreeGCFixture(t)
	instances, routes, err := store.PruneWorktreeInstances(context.Background(), time.Now().UTC(), 30*24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("PruneWorktreeInstances: %v", err)
	}
	if instances != 2 {
		t.Fatalf("pruned %d instances, want 2 (deleted + creating)", instances)
	}
	if routes != 2 {
		t.Fatalf("pruned %d routes, want 2", routes)
	}
	states := instanceStates(t, store)
	if states["deleted"] || states["creating"] {
		t.Fatalf("terminal/abandoned instances survived: %v", states)
	}
	if !states["active"] || !states["deleting"] {
		t.Fatalf("live instances were reaped: %v", states)
	}
	if got := routeCount(t, store); got != 2 {
		t.Fatalf("routes = %d, want 2 (active + deleting)", got)
	}
}

// TestPruneWorktreeInstancesHonoursRetention proves a young store loses
// nothing: a worktree created seconds ago is not garbage.
func TestPruneWorktreeInstancesHonoursRetention(t *testing.T) {
	store, _ := worktreeGCFixture(t)
	ctx := context.Background()
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `UPDATE worktree_instances SET updated_at=?`, fresh); err != nil {
		t.Fatal(err)
	}
	instances, routes, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), 30*24*time.Hour, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if instances != 0 || routes != 0 {
		t.Fatalf("pruned %d instances / %d routes inside the window, want 0/0", instances, routes)
	}
}

// TestPruneWorktreeInstancesReapsOrphanedRoutes covers the dangling rows a
// crash leaves behind: a route pointing at an instance row that is gone can
// never resolve, so it is garbage regardless of age.
func TestPruneWorktreeInstancesReapsOrphanedRoutes(t *testing.T) {
	store, principal := worktreeGCFixture(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,?)`,
		principal.WorkspaceID, principal.SubjectID, "ghost", "/tmp/ghost", old, old, "wt_missing"); err != nil {
		t.Fatal(err)
	}
	if _, routes, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), 30*24*time.Hour, 1000); err != nil {
		t.Fatal(err)
	} else if routes != 3 {
		t.Fatalf("pruned %d routes, want 3 (2 owned + 1 orphan)", routes)
	}
	var ghosts int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_routes WHERE worktree='ghost'`).Scan(&ghosts); err != nil {
		t.Fatal(err)
	}
	if ghosts != 0 {
		t.Fatal("orphaned route survived")
	}
}

// TestPruneWorktreeInstancesSparesLegacyRoutes: an instance_id IS NULL route
// is the pre-instance (legacy) binding, not an orphan. It must never be
// mistaken for a dangling row.
func TestPruneWorktreeInstancesSparesLegacyRoutes(t *testing.T) {
	store, principal := worktreeGCFixture(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO worktree_routes(workspace_id,subject_id,worktree,dir,created_at,updated_at,instance_id) VALUES(?,?,?,?,?,?,NULL)`,
		principal.WorkspaceID, principal.SubjectID, "legacy", "/tmp/legacy", old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), 30*24*time.Hour, 1000); err != nil {
		t.Fatal(err)
	}
	var legacy int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktree_routes WHERE worktree='legacy'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != 1 {
		t.Fatal("legacy instance-less route was reaped")
	}
}

// TestPruneWorktreeInstancesIsIdempotent keeps the sweep safely re-runnable.
func TestPruneWorktreeInstancesIsIdempotent(t *testing.T) {
	store, _ := worktreeGCFixture(t)
	ctx := context.Background()
	if _, _, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), 30*24*time.Hour, 1000); err != nil {
		t.Fatal(err)
	}
	instances, routes, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), 30*24*time.Hour, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if instances != 0 || routes != 0 {
		t.Fatalf("second sweep removed %d instances / %d routes, want 0/0", instances, routes)
	}
}

// TestPruneWorktreeInstancesRejectsBadBounds keeps the guard fail-closed.
func TestPruneWorktreeInstancesRejectsBadBounds(t *testing.T) {
	store, _ := worktreeGCFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		retention time.Duration
		limit     int
	}{{-time.Hour, 10}, {time.Hour, 0}, {time.Hour, 10_001}} {
		if _, _, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), tc.retention, tc.limit); err == nil {
			t.Fatalf("retention=%v limit=%d: expected rejection", tc.retention, tc.limit)
		}
	}
	if got := len(instanceStates(t, store)); got != 4 {
		t.Fatalf("rejected sweeps changed rows: %d states left, want 4", got)
	}
}
