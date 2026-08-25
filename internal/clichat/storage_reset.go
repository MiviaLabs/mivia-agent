package clichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// RunStorage dispatches `mivia storage <subcommand>`.
func RunStorage(args []string) error {
	return runStorageWithIO(args, os.Stdout, os.Stderr)
}

func runStorageWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("storage: expected reset")
	}
	switch subcommand, rest := args[0], args[1:]; subcommand {
	case "reset":
		return runStorageReset(rest, stdout, stderr)
	default:
		return fmt.Errorf("storage: unknown subcommand %q", cliagents.SafeCatalogText(subcommand, 80))
	}
}

// runStorageReset implements `mivia storage reset`: an irreversible wipe of
// every durable session/context/workflow row in this workspace, keeping only
// the store's migrated schema and never touching the separate memory files
// (memory.db, org.db - internal/memory, a different file this command never
// opens).
//
// Dry-run by default: prints per-table row counts and the files that would
// be wiped and preserved, and exits 0 with no writes. --yes performs the
// wipe. Both paths are gated behind the hub's election lock (internal/hub),
// the same guard sessions gc --compact uses, so this refuses to run while
// any interactive mivia process (TUI, REPL, desktop sidecar) is joined to
// the workspace - narrowing, not eliminating, the risk of racing a
// concurrent writer; see hub.TryAcquireMaintenanceLock's doc comment for the
// residual gap (a one-shot CLI invocation is not caught by this check).
func runStorageReset(args []string, stdout, stderr io.Writer) error {
	rest := args
	yes := false
	filtered := rest[:0]
	for _, arg := range rest {
		if arg == "--yes" {
			yes = true
			continue
		}
		filtered = append(filtered, arg)
	}
	rest = filtered

	workspaceRoot, jsonFlag, _, err := parseSessionsWorkspaceAndJSON("storage reset", rest, 0)
	if err != nil {
		return err
	}
	_, contextStore, root, res, err := newCatalogSessionAt(workspaceRoot)
	if err != nil {
		return fmt.Errorf("storage reset: %w", err)
	}
	defer contextStore.Close()

	orchestrationPath := orchestrationStorePathFor(res, root)
	stores := []*storage.SQLite{contextStore}
	var orchestrationStore *storage.SQLite
	if filepath.Clean(orchestrationPath) != filepath.Clean(contextStore.Path()) {
		orchestrationStore, err = storage.OpenSQLite(orchestrationPath)
		if err != nil {
			return fmt.Errorf("storage reset: open orchestration store: %w", err)
		}
		defer orchestrationStore.Close()
		stores = append(stores, orchestrationStore)
	}

	preserved := []string{workspace.MemoryDBPath(root)}
	if org := workspace.OrgMemoryDBPath(); org != "" {
		preserved = append(preserved, org)
	}

	if !yes {
		return reportStorageResetDryRun(stdout, stores, preserved, jsonFlag)
	}

	release, ok := hub.TryAcquireMaintenanceLock(filepath.Dir(contextStore.Path()))
	if !ok {
		err := fmt.Errorf("storage reset: refusing: another mivia process (chat, TUI, or desktop) is using this workspace - close it and retry")
		fmt.Fprintln(stderr, err)
		return err
	}
	defer release()

	ctx := context.Background()
	for _, store := range stores {
		if err := store.WipeAllExceptSchema(ctx); err != nil {
			fmt.Fprintf(stderr, "storage reset: %v\n", err)
			return fmt.Errorf("storage reset: %w", err)
		}
		if err := store.Compact(ctx); err != nil {
			fmt.Fprintf(stderr, "storage reset: %v\n", err)
			return fmt.Errorf("storage reset: %w", err)
		}
	}

	if jsonFlag {
		return writeSessionsJSON(stdout, map[string]any{
			"wiped":     storePaths(stores),
			"preserved": preserved,
		})
	}
	for _, path := range storePaths(stores) {
		fmt.Fprintf(stdout, "wiped %s\n", path)
	}
	for _, path := range preserved {
		fmt.Fprintf(stdout, "preserved %s\n", path)
	}
	return nil
}

// orchestrationStorePathFor mirrors how a real `mivia workflow run` in this
// workspace resolves its store, without opening it: clone res so the
// context store's own already-resolved StorePath is untouched, then apply
// the same pin cliworkflow.ApplyWorkflowStoreRoot uses.
func orchestrationStorePathFor(res *config.Resolved, root string) string {
	clone := *res
	cliworkflow.ApplyWorkflowStoreRoot(&clone, root)
	return clone.Subagents.StorePath
}

func storePaths(stores []*storage.SQLite) []string {
	paths := make([]string, len(stores))
	for i, store := range stores {
		paths[i] = store.Path()
	}
	return paths
}

func reportStorageResetDryRun(stdout io.Writer, stores []*storage.SQLite, preserved []string, jsonFlag bool) error {
	ctx := context.Background()
	counts := make(map[string]map[string]int, len(stores))
	for _, store := range stores {
		c, err := store.TableRowCounts(ctx)
		if err != nil {
			return fmt.Errorf("storage reset: %w", err)
		}
		counts[store.Path()] = c
	}
	if jsonFlag {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"dry_run":    true,
			"would_wipe": counts,
			"preserved":  preserved,
		})
	}
	fmt.Fprintln(stdout, "dry run - pass --yes to actually wipe. Would remove:")
	for path, tables := range counts {
		total := 0
		for _, n := range tables {
			total += n
		}
		fmt.Fprintf(stdout, "  %s: %d row(s) across %d table(s)\n", path, total, len(tables))
	}
	fmt.Fprintln(stdout, "would preserve:")
	for _, path := range preserved {
		fmt.Fprintf(stdout, "  %s\n", path)
	}
	return nil
}
