package cliautomations

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// runRunCommand implements `mivia automations run <id> [--wait]
// [--workspace dir] [--config path]`: a one-shot manual fire of
// automation id. CloseLastRun is called explicitly (deferred, so it
// also runs on the error path) rather than relying on process exit,
// because an unclosed SQLite handle skips its WAL checkpoint on close -
// see HeadlessSpawner's own doc comment for the sequential-dispatch
// invariant this relies on.
func runRunCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	// --wait is verbosity-only: RunOnce runs synchronously end-to-end
	// already; --wait is retained for D14 surface compatibility and
	// controls output detail only, never blocking behavior - a real
	// async/backgrounded run mode is out of scope for this chunk.
	wait, rest := parseWaitFlag(rest)
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
	if wait {
		printRunDetail(run)
	} else {
		printRunResult(run)
	}
	if runFailed(run.State) {
		return fmt.Errorf("automations run: %s failed: %s", id, run.Message)
	}
	return nil
}

// parseWaitFlag parses the bare boolean --wait flag, removing it from
// args. Reuses flagValueSeam's own arg-scanning shape rather than a
// separate flag.NewFlagSet: this package's parseCommonFlags is itself a
// small hand-rolled scanner (flagValueSeam), not a flag.FlagSet, so a
// local bare-boolean scanner matches its existing convention rather than
// introducing a second flag-parsing style. Unlike flagValueSeam, a bare
// boolean flag has no malformed-input case to reject, so this returns
// no error.
func parseWaitFlag(args []string) (bool, []string) {
	out := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == "--wait" {
			found = true
			continue
		}
		out = append(out, a)
	}
	return found, out
}
