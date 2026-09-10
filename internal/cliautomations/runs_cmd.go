package cliautomations

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// defaultRunsLimit is `automations runs`'s own --limit default, applied
// both to the single-automation path (svc.Runs(id, limit) directly) and
// the cross-automation path (each per-automation Runs(id, limit) call,
// before the combined slice is truncated to limit again below).
const defaultRunsLimit = 20

// runRunsCommand implements `mivia automations runs [--automation <id>]
// [--limit <n>] [--workspace dir] [--config path]`.
//
// ports.AutomationSettings has no cross-automation run-list method (and
// none is added here - see runs_cmd_test.go's own scope note): when
// --automation is unset, this command itself iterates svc.Automations()
// and concatenates each automation's svc.Runs(id, limit) client-side,
// sorts the combined slice by StartedAt descending, then truncates to
// limit. This is CLI-only aggregation, not a new Service capability.
func runRunsCommand(args []string) error {
	workspaceRoot, configPath, rest, err := parseCommonFlags(args)
	if err != nil {
		return err
	}
	automationID, rest, _, err := flagValueSeam(rest, "--automation")
	if err != nil {
		return err
	}
	limitStr, rest, limitFound, err := flagValueSeam(rest, "--limit")
	if err != nil {
		return err
	}
	limit := defaultRunsLimit
	if limitFound {
		parsed, perr := strconv.Atoi(limitStr)
		if perr != nil {
			return fmt.Errorf("automations runs: --limit expects an integer, got %q", limitStr)
		}
		limit = parsed
	}
	if len(rest) != 0 {
		return fmt.Errorf("automations runs: unexpected arguments %v", rest)
	}

	svc, _, cleanup, err := buildService(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	var runs []ports.Run
	if automationID != "" {
		runs = svc.Runs(automationID, limit)
	} else {
		for _, a := range svc.Automations() {
			runs = append(runs, svc.Runs(a.ID, limit)...)
		}
		sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
		if len(runs) > limit {
			runs = runs[:limit]
		}
	}
	printRunList(runs)
	return nil
}
