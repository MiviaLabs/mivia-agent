package cliautomations

import (
	"fmt"
	"io"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// stdoutWriter/stderrWriter are indirections so tests can capture
// output without redirecting the real os.Stdout/os.Stderr.
var stdoutWriter = func() io.Writer { return os.Stdout }
var stderrWriter = func() io.Writer { return os.Stderr }

// runStateString renders a ports.RunState as a short lowercase label.
func runStateString(s ports.RunState) string {
	switch s {
	case ports.RunPending:
		return "pending"
	case ports.RunRunning:
		return "running"
	case ports.RunSucceeded:
		return "succeeded"
	case ports.RunFailed:
		return "failed"
	case ports.RunCancelled:
		return "cancelled"
	case ports.RunInterrupted:
		return "interrupted"
	case ports.RunSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// runFailed reports whether a run's terminal state should be treated as
// a CLI failure (non-zero exit): failed only. Skipped/cancelled are not
// treated as process failures here - skipped is a documented no-op
// (D7), and cancelled reflects an operator action, not a fault.
func runFailed(s ports.RunState) bool {
	return s == ports.RunFailed
}

func printAutomationList(automations []ports.Automation) {
	w := stdoutWriter()
	if len(automations) == 0 {
		fmt.Fprintln(w, "no automations defined")
		return
	}
	for _, a := range automations {
		status := "enabled"
		if !a.Enabled {
			status = "disabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", a.ID, status, a.Name)
	}
}

func printAutomationDetail(a ports.Automation, runs []ports.Run) {
	w := stdoutWriter()
	fmt.Fprintf(w, "id: %s\n", a.ID)
	fmt.Fprintf(w, "name: %s\n", a.Name)
	fmt.Fprintf(w, "enabled: %v\n", a.Enabled)
	if a.Description != "" {
		fmt.Fprintf(w, "description: %s\n", a.Description)
	}
	fmt.Fprintf(w, "runs:\n")
	if len(runs) == 0 {
		fmt.Fprintf(w, "  (none)\n")
		return
	}
	for _, r := range runs {
		fmt.Fprintf(w, "  %s\t%s\tstarted=%s\n", r.ID, runStateString(r.State), r.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
}

func printRunResult(r ports.Run) {
	w := stdoutWriter()
	fmt.Fprintf(w, "run_id=%s state=%s\n", r.ID, runStateString(r.State))
	if r.Message != "" {
		fmt.Fprintf(w, "message: %s\n", r.Message)
	}
}
