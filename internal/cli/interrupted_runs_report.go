package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// staleInterruptedRunAge is how long an interrupted run stays newsworthy at
// startup. Recover cannot mark a run as already-reported - there is no
// non-terminal "interrupted" status, and every terminal one makes a resume
// refuse - so classification is recomputed on every launch and a run abandoned
// days ago was announced forever. Age is what separates "you just lost this" from
// "this has been dead for a week"; older runs stay reachable through /resume.
const staleInterruptedRunAge = 12 * time.Hour

// formatInterruptedRunReport returns the single startup line describing runs a
// previous process left unfinished, or "" when there is nothing worth saying.
//
// One line regardless of count: the previous code printed one per run with no
// cap, so a workspace with a backlog buried the startup banner.
func formatInterruptedRunReport(recovered []ledger.RecoveredRun, now time.Time) string {
	recent := 0
	for _, r := range recovered {
		if !r.WasInterrupted {
			continue
		}
		// A zero CreatedAt predates timestamp stamping; treat it as stale rather
		// than announcing a run whose age cannot be established.
		if r.CreatedAt.IsZero() || now.Sub(r.CreatedAt) > staleInterruptedRunAge {
			continue
		}
		recent++
	}
	if recent == 0 {
		return ""
	}
	noun := "runs"
	if recent == 1 {
		noun = "run"
	}
	return fmt.Sprintf("info: %d unfinished orchestration %s from an earlier process; /resume to list\n", recent, noun)
}

// reportInterruptedRuns writes the startup report for a Recover result. Shared by
// every construction path so the two surfaces cannot drift.
func reportInterruptedRuns(w io.Writer, recovered []ledger.RecoveredRun, recErr error) {
	if recErr != nil {
		fmt.Fprintf(w, "warning: orchestration recovery error: %v\n", recErr)
		return
	}
	if line := formatInterruptedRunReport(recovered, time.Now()); line != "" {
		fmt.Fprint(w, line)
	}
}

// formatRunAge renders a coarse age for a run listing: a two-day-old run offered
// with no age reads as something that just broke.
func formatRunAge(createdAt, now time.Time) string {
	if createdAt.IsZero() {
		return "age unknown"
	}
	d := now.Sub(createdAt)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
