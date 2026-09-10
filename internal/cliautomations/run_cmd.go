package cliautomations

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// runRunCommand implements `mivia automations run <id> [--workspace
// dir] [--config path]`: a one-shot manual fire of automation id.
// CloseLastRun is called explicitly (deferred, so it also runs on the
// error path) rather than relying on process exit, because an unclosed
// SQLite handle skips its WAL checkpoint on close - see
// HeadlessSpawner's own doc comment for the sequential-dispatch
// invariant this relies on.
func runRunCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("automations run: expected exactly one automation id")
	}
	id := rest[0]

	svc, spawn, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	run, runErr := svc.RunOnce(ctx, id, ports.TriggerManual)
	if closeErr := spawn.CloseLastRun(); closeErr != nil {
		fmt.Fprintf(stderrWriter(), "automations run: close session store: %v\n", closeErr)
	}
	if runErr != nil {
		return runErr
	}
	printRunResult(run)
	if runFailed(run.State) {
		return fmt.Errorf("automations run: %s failed: %s", id, run.Message)
	}
	return nil
}
