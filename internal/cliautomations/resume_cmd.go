package cliautomations

import (
	"context"
	"fmt"
)

// runResumeCommand implements `mivia automations resume <run-id>
// [--workspace dir] [--config path]`: restarts an interrupted or failed
// run from exactly the step it stopped at (D13, chunk 8's
// Service.ResumeRun). Mirrors run_cmd.go's runRunCommand shape
// precisely, including its CloseLastRun ordering rationale: CloseLastRun
// is called explicitly (deferred, so it also runs on the error path)
// rather than relying on process exit, because an unclosed SQLite handle
// skips its WAL checkpoint on close - see HeadlessSpawner's own doc
// comment for the sequential-dispatch invariant this relies on.
func runResumeCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("automations resume: expected exactly one run id")
	}
	runID := rest[0]

	svc, spawn, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	run, runErr := svc.ResumeRun(ctx, runID)
	if closeErr := spawn.CloseLastRun(); closeErr != nil {
		fmt.Fprintf(stderrWriter(), "automations resume: close session store: %v\n", closeErr)
	}
	if runErr != nil {
		return runErr
	}
	printRunResult(run)
	if runFailed(run.State) {
		return fmt.Errorf("automations resume: %s failed: %s", runID, run.Message)
	}
	return nil
}
