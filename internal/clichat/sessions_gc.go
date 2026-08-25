package clichat

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

const (
	// defaultCheckpointRetentionDays matches the retention Claude Code applies
	// to its own session transcripts. It bounds a table that was previously
	// unbounded; a shorter window is a user choice, not a safer default.
	defaultCheckpointRetentionDays = 30
	// defaultCheckpointKeep is the per-session resume floor: however old a
	// session is, its newest checkpoints stay reachable.
	defaultCheckpointKeep = 3
	// checkpointGCBatch bounds one sweep so the write lock is never held for
	// an unbounded delete. runSessionsGC loops until a sweep comes back short.
	checkpointGCBatch = 1000
)

// runSessionsGC prunes aged context checkpoints and, with --compact, rewrites
// the database file to give the freed pages back to the filesystem.
//
// This is the only bound on context_checkpoints. Every committed turn writes
// one row carrying a full active_context blob, so an unswept store grows
// without limit - the behaviour that put 144 MB of checkpoints in a 311 MB
// database over ten days.
func runSessionsGC(args []string, stdout, stderr io.Writer) error {
	retentionDays := defaultCheckpointRetentionDays
	keep := defaultCheckpointKeep
	compact := false

	rest := args
	var raw string
	var found bool
	var err error
	if raw, rest, found, err = FlagValueFunc(rest, "--keep-days"); err != nil {
		return fmt.Errorf("sessions gc: %w", err)
	} else if found {
		if retentionDays, err = parseNonNegativeCount(raw, "--keep-days"); err != nil {
			return fmt.Errorf("sessions gc: %w", err)
		}
	}
	if raw, rest, found, err = FlagValueFunc(rest, "--keep"); err != nil {
		return fmt.Errorf("sessions gc: %w", err)
	} else if found {
		if keep, err = parseNonNegativeCount(raw, "--keep"); err != nil {
			return fmt.Errorf("sessions gc: %w", err)
		}
	}
	filtered := rest[:0]
	for _, arg := range rest {
		if arg == "--compact" {
			compact = true
			continue
		}
		filtered = append(filtered, arg)
	}
	rest = filtered

	workspaceRoot, jsonFlag, _, err := parseSessionsWorkspaceAndJSON("sessions gc", rest, 0)
	if err != nil {
		return err
	}
	_, store, err := newCatalogSession(workspaceRoot)
	if err != nil {
		return fmt.Errorf("sessions gc: %w", err)
	}
	defer store.Close()

	retention := time.Duration(retentionDays) * 24 * time.Hour
	removed, err := sweepCheckpoints(context.Background(), store, retention, keep)
	if err != nil {
		fmt.Fprintf(stderr, "sessions gc: %v\n", err)
		return fmt.Errorf("sessions gc: %w", err)
	}
	instances, routes, err := sweepWorktrees(context.Background(), store, retention)
	if err != nil {
		fmt.Fprintf(stderr, "sessions gc: %v\n", err)
		return fmt.Errorf("sessions gc: %w", err)
	}
	if compact {
		if err := store.Compact(context.Background()); err != nil {
			fmt.Fprintf(stderr, "sessions gc: %v\n", err)
			return fmt.Errorf("sessions gc: %w", err)
		}
	}
	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{
			"removed_checkpoints":        removed,
			"removed_worktree_instances": instances,
			"removed_worktree_routes":    routes,
			"retention_days":             retentionDays,
			"keep_per_session":           keep,
			"compacted":                  compact,
		})
	}
	fmt.Fprintf(stdout, "removed %d checkpoint(s) older than %d day(s), keeping %d per session\n", removed, retentionDays, keep)
	fmt.Fprintf(stdout, "removed %d worktree instance(s) and %d route(s)\n", instances, routes)
	if compact {
		fmt.Fprintln(stdout, "compacted the database file")
	}
	return nil
}

// sweepCheckpoints repeats the bounded prune until a pass comes back short,
// so one command fully sweeps a store that has never been swept before.
func sweepCheckpoints(ctx context.Context, store *storage.SQLite, retention time.Duration, keep int) (int, error) {
	total := 0
	for {
		removed, err := store.PruneSessionCheckpoints(ctx, time.Now().UTC(), retention, keep, checkpointGCBatch)
		if err != nil {
			return total, err
		}
		total += removed
		if removed < checkpointGCBatch {
			return total, nil
		}
	}
}

// sweepWorktrees repeats the bounded worktree prune until a pass comes back
// short, matching sweepCheckpoints.
func sweepWorktrees(ctx context.Context, store *storage.SQLite, retention time.Duration) (int, int, error) {
	totalInstances, totalRoutes := 0, 0
	for {
		instances, routes, err := store.PruneWorktreeInstances(ctx, time.Now().UTC(), retention, checkpointGCBatch)
		if err != nil {
			return totalInstances, totalRoutes, err
		}
		totalInstances += instances
		totalRoutes += routes
		if instances < checkpointGCBatch {
			return totalInstances, totalRoutes, nil
		}
	}
}

func parseNonNegativeCount(raw, flag string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", flag, raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s: must not be negative", flag)
	}
	return n, nil
}
