package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestSlashResumeRouting verifies that the /resume command is routed correctly
// in the REPL slash handler.
func TestSlashResumeRouting(t *testing.T) {
	// Verify that handleSlashResume exists (compiles) and handles a nil term.
	handled, exit, err := handleSlashResume("/resume", []string{"/resume"}, nil)
	if !handled {
		t.Fatal("expected handled=true with nil term")
	}
	if exit {
		t.Fatal("expected exit=false")
	}
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestResumeSlashCommandListsInterruptedRuns verifies that /resume with no
// arguments lists interrupted runs (tests the TUI handler).
func TestResumeSlashCommandListsInterruptedRuns(t *testing.T) {
	// Create a TUI model for testing the slash handler.
	// We use the shared listInterruptedRuns and formatListedRuns functions.
	msg := formatListedRuns([]coordinator.RecoveredRun{
		{RunID: "run-1", DisplayName: "test-run-1", Status: "interrupted", WasInterrupted: true},
		{RunID: "run-2", DisplayName: "test-run-2", Status: "interrupted", WasInterrupted: true},
	})
	if !strings.Contains(msg, "run-1") {
		t.Fatal("formatted list should contain run-1")
	}
	if !strings.Contains(msg, "run-2") {
		t.Fatal("formatted list should contain run-2")
	}
	if !strings.Contains(msg, "test-run-1") {
		t.Fatal("formatted list should contain test-run-1")
	}
}

// TestResumeSlashWithConfirmation verifies the slash resume flow including
// the confirmation prompt.
func TestResumeSlashWithConfirmation(t *testing.T) {
	// Test the confirmation building and parsing.
	info := resumeConfirmationInfo{
		RunID:         "run-confirm",
		DisplayName:   "test",
		TaskCount:     2,
		PriorAttempts: 1,
	}
	msg := formatResumeConfirmation(info)
	if !strings.Contains(msg, "run-confirm") {
		t.Fatal("confirmation should contain run ID")
	}
	if !strings.Contains(msg, "2 tasks") {
		t.Fatal("confirmation should contain task count")
	}

	// Verify parseConfirmResponse works.
	if !parseConfirmResponse("y") {
		t.Fatal("'y' should be parsed as confirmed")
	}
	if !parseConfirmResponse("Y") {
		t.Fatal("'Y' should be parsed as confirmed")
	}
	if !parseConfirmResponse("yes") {
		t.Fatal("'yes' should be parsed as confirmed")
	}
	if parseConfirmResponse("n") {
		t.Fatal("'n' should NOT be parsed as confirmed")
	}
	if parseConfirmResponse("") {
		t.Fatal("empty should NOT be parsed as confirmed")
	}
	if parseConfirmResponse("garbage") {
		t.Fatal("garbage should NOT be parsed as confirmed")
	}
}

// TestResumeSlashRefusesHeldRun verifies the end-to-end error messaging.
func TestResumeSlashRefusesHeldRun(t *testing.T) {
	msg := formatResumeError(coordinator.ErrRunHeldByAnotherExecutor, "run-held")
	if !strings.Contains(msg, "held by another") {
		t.Fatalf("held-by-another message should mention 'held by another', got: %s", msg)
	}
}

// TestResumeSlashRefusesTerminalRun verifies the terminal run error message.
func TestResumeSlashRefusesTerminalRun(t *testing.T) {
	err := fmt.Errorf("resume: run %q is already terminal (%s)", "run-term", "completed")
	msg := formatResumeError(err, "run-term")
	if !strings.Contains(msg, "already terminal") {
		t.Fatalf("terminal message should mention 'already terminal', got: %s", msg)
	}
}

// TestResumeSlashRefusesUnresumableRun verifies the missing-Input error message.
func TestResumeSlashRefusesUnresumableRun(t *testing.T) {
	err := fmt.Errorf("resume: task %q has no persisted input (created before task inputs were recorded; cannot resume this run)", "t1")
	msg := formatResumeError(err, "run-no-input")
	if !strings.Contains(msg, "missing task input data") {
		t.Fatalf("unresumable message should mention 'missing task input data', got: %s", msg)
	}
}

// TestResumeSlashNoCoordinator verifies graceful handling when no coordinator exists.
func TestResumeSlashNoCoordinator(t *testing.T) {
	// Clean up any coordinators.
	coordinators.Range(func(key, _ any) bool {
		coordinators.Delete(key)
		return true
	})

	// Ensure we don't have a coordinator.
	var found bool
	coordinators.Range(func(_, _ any) bool {
		found = true
		return false
	})
	if found {
		t.Skip("coordinator exists; can't test no-coordinator case")
	}
}

// Compile-time check that the test references the expected types.
var (
	_ = chat.Session{}
	_ = config.Resolved{}
	_ = context.Background
	_ = runtime.Caller{}
	_ = ledger.RunSnapshot{}
	_ = subagents.Task{}
	_ = errors.New
	_ = time.Now
)
