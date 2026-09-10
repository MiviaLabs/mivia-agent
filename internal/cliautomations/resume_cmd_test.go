package cliautomations

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestResumeCommandRequiresExactlyOneRunID proves `automations resume`
// rejects both zero and two-or-more positional args, mirroring
// run_cmd.go's own exactly-one-id contract. Both branches return before
// buildService is ever called (resume_cmd.go checks len(rest) first),
// so a bare t.TempDir() with no config fixture is sufficient here.
func TestResumeCommandRequiresExactlyOneRunID(t *testing.T) {
	root := t.TempDir()
	if err := runResumeCommand([]string{"--workspace", root}); err == nil {
		t.Fatal("runResumeCommand with no run id: got nil error, want rejection")
	}
	if err := runResumeCommand([]string{"--workspace", root, "run-a", "run-b"}); err == nil {
		t.Fatal("runResumeCommand with two run ids: got nil error, want rejection")
	}
}

// TestResumeCommandUnknownRunIDErrors proves an unknown run id's
// automation.ErrRunNotFound propagates un-swallowed: resume_cmd.go
// returns svc.ResumeRun's own runErr verbatim (no re-wrap), so
// errors.Is must still find the sentinel.
func TestResumeCommandUnknownRunIDErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "resume-unknown")
	err := runResumeCommand([]string{"--workspace", root, "no-such-run"})
	if err == nil {
		t.Fatal("runResumeCommand(unknown run id): got nil error, want automation.ErrRunNotFound")
	}
	if !errors.Is(err, automation.ErrRunNotFound) {
		t.Fatalf("runResumeCommand(unknown run id) error = %v, want it to wrap automation.ErrRunNotFound", err)
	}
}

// TestResumeCommandClosesSessionStoreOnErrorPath mirrors run_cmd_test.
// go's TestRunCommandCallsCloseLastRunOnRunOnceError: resume_cmd.go
// calls spawn.CloseLastRun() unconditionally, even on ResumeRun's error
// path (here, an unknown run id, which resume_cmd.go's exact source
// order guarantees never reaches spawn.CreateFreshInDir at all - see
// resume.go's getRun/ErrRunNotFound check firing before any spawn call).
// CloseLastRun on an idle spawner must be a clean no-op: no
// "close session store" line on stderr, and the original ResumeRun
// error must still be the one returned, not swallowed by a close
// failure.
func TestResumeCommandClosesSessionStoreOnErrorPath(t *testing.T) {
	root := writeAutomationsFixture(t, "resume-close-on-error")
	var resumeErr error
	_, stderr := captureOutput(t, func() {
		resumeErr = runResumeCommand([]string{"--workspace", root, "no-such-run-at-all"})
	})
	if resumeErr == nil {
		t.Fatal("runResumeCommand(unknown run id): got nil error, want it propagated")
	}
	if !errors.Is(resumeErr, automation.ErrRunNotFound) {
		t.Fatalf("runResumeCommand(unknown run id) error = %v, want automation.ErrRunNotFound", resumeErr)
	}
	if strings.Contains(stderr, "close session store") {
		t.Fatalf("runResumeCommand stderr = %q, want no close-session-store error (CloseLastRun on an idle spawner is a clean no-op)", stderr)
	}
}

// TestResumeCommandParseFlagsErrorPropagates mirrors run_cmd_test.go's
// TestRunRunCommandParseFlagsErrorPropagates.
func TestResumeCommandParseFlagsErrorPropagates(t *testing.T) {
	err := runResumeCommand([]string{"--workspace"})
	if err == nil {
		t.Fatal("runResumeCommand with --workspace missing its value: got nil error")
	}
}

// TestResumeCommandBuildServiceErrorPropagates mirrors run_cmd_test.go's
// TestRunRunCommandBuildServiceErrorPropagates.
func TestResumeCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runResumeCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist"), "any-run-id"})
	if err == nil {
		t.Fatal("runResumeCommand against a nonexistent workspace: got nil error")
	}
}

// TestResumeCommandSucceedsPrintsRunResult exercises resume.go's
// cheapest real success path: a run whose StepIndex already covers
// every step of its automation (run.StepIndex >= len(spec.Steps)) takes
// ResumeRun's "already complete" branch (markResumedRunSucceeded) and
// returns RunSucceeded WITHOUT ever spawning a session or touching the
// fenced claim - so a directly-inserted run row (via
// runs_cmd_test.go's insertAutomationRun helper, same package) is
// sufficient; a full spawn+Load session round trip is not needed to
// reach and observe printRunResult's success output.
func TestResumeCommandSucceedsPrintsRunResult(t *testing.T) {
	root := writeAutomationsFixture(t, "resume-already-done")
	insertAutomationRun(t, root, storage.AutomationRun{
		ID:           "run-already-done",
		AutomationID: "resume-already-done",
		Origin:       "manual",
		State:        "interrupted",
		StepIndex:    1, // == len(spec.Steps): writeAutomationsFixture seeds exactly one StepPrompt step
		StepCount:    1,
		StartedAt:    time.Now().UTC().Format(time.RFC3339),
	})

	stdout, _ := captureOutput(t, func() {
		if err := runResumeCommand([]string{"--workspace", root, "run-already-done"}); err != nil {
			t.Fatalf("runResumeCommand: %v", err)
		}
	})
	if !strings.Contains(stdout, "run_id=run-already-done") {
		t.Fatalf("runResumeCommand stdout = %q, want run_id=run-already-done", stdout)
	}
	if !strings.Contains(stdout, "state=succeeded") {
		t.Fatalf("runResumeCommand stdout = %q, want state=succeeded", stdout)
	}
}
