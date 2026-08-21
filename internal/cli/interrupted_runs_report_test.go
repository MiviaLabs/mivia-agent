package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

func interruptedAt(id string, age time.Duration, now time.Time) ledger.RecoveredRun {
	return ledger.RecoveredRun{
		RunID:          id,
		DisplayName:    id,
		Status:         ledger.RunStatusQueued,
		WasInterrupted: true,
		CreatedAt:      now.Add(-age),
	}
}

// TestInterruptedRunReportSkipsStaleRuns locks the startup noise. Recover
// classifies every non-terminal run as interrupted on every launch and cannot
// mark them otherwise - there is no non-terminal "interrupted" status, and the
// three terminal ones all refuse a resume, so mutating them would destroy the
// recoverability the report exists to advertise. A run abandoned days ago is
// therefore announced forever. It stays resumable via /resume; it stops nagging.
func TestInterruptedRunReportSkipsStaleRuns(t *testing.T) {
	now := time.Now()
	stale := []ledger.RecoveredRun{
		interruptedAt("run-1", 48*time.Hour, now),
		interruptedAt("run-2", 30*24*time.Hour, now),
	}
	if got := formatInterruptedRunReport(stale, now); got != "" {
		t.Fatalf("stale interrupted runs must not be announced, got %q", got)
	}
}

// TestInterruptedRunReportAnnouncesRecent - a run interrupted moments ago is news:
// the user just lost it and can get it back.
func TestInterruptedRunReportAnnouncesRecent(t *testing.T) {
	now := time.Now()
	got := formatInterruptedRunReport([]ledger.RecoveredRun{
		interruptedAt("run-9", 2*time.Minute, now),
	}, now)
	if got == "" {
		t.Fatal("a freshly interrupted run must be announced")
	}
	if !strings.Contains(got, "/resume") {
		t.Errorf("the report must say how to act on it, got %q", got)
	}
}

// TestInterruptedRunReportIsBounded - the old code printed one line per run with
// no cap, so a workspace with many interrupted runs buried the startup banner.
func TestInterruptedRunReportIsBounded(t *testing.T) {
	now := time.Now()
	var many []ledger.RecoveredRun
	for i := 0; i < 50; i++ {
		many = append(many, interruptedAt("run-"+itoa(i), time.Minute, now))
	}
	got := formatInterruptedRunReport(many, now)
	if got == "" {
		t.Fatal("recent interrupted runs must be announced")
	}
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 0 {
		t.Fatalf("report must be a single line regardless of run count, got %d extra lines:\n%s", n, got)
	}
	if !strings.Contains(got, "50") {
		t.Errorf("report should carry the count, got %q", got)
	}
}

// TestInterruptedRunReportIgnoresTerminalRuns - Recover returns every run, not
// only interrupted ones.
func TestInterruptedRunReportIgnoresTerminalRuns(t *testing.T) {
	now := time.Now()
	done := ledger.RecoveredRun{
		RunID:          "run-done",
		Status:         ledger.RunStatusCompleted,
		WasInterrupted: false,
		CreatedAt:      now.Add(-time.Minute),
	}
	if got := formatInterruptedRunReport([]ledger.RecoveredRun{done}, now); got != "" {
		t.Fatalf("completed runs must not be reported as interrupted, got %q", got)
	}
}

// TestFormatListedRunsShowsAge - /resume remains the only way to reach a stale
// run, so the listing must say how old each one is. A two-day-dead run offered
// with no age reads as something that just broke.
func TestFormatListedRunsShowsAge(t *testing.T) {
	out := FormatListedRuns([]coordinator.RecoveredRun{{
		RunID:       "run-1",
		DisplayName: "audit",
		CreatedAt:   time.Now().Add(-48 * time.Hour),
	}})
	if !strings.Contains(out, "run-1") {
		t.Fatalf("listing lost the run id: %q", out)
	}
	if !strings.Contains(out, "2d") {
		t.Errorf("listing must show the run's age so a dead run is recognisable, got %q", out)
	}
}
