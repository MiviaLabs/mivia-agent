package cliautomations

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
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

// TestResumeCommandReturnsErrorOnRealResumeFailure covers
// resume_cmd.go's own runFailed(run.State) branch: a resume that
// genuinely fails (not a not-found/not-resumable refusal) must still
// print the run result AND return a non-nil error naming the failure.
//
// Built by driving a REAL RunOnce to a genuine RunFailed state first
// (against a failing stub provider - this durably saves the run's
// session before the step fails, per spawnRunSession's own Save-before-
// steps ordering), then resuming that exact run id against the SAME
// still-failing provider, so ResumeRun's own step dispatch fails again
// for real.
func TestResumeCommandReturnsErrorOnRealResumeFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	stubURL := failingStubProviderServer(t)
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("CLIAUTOMATIONS_RESUME_FAIL_TEST_KEY", "test-key")
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "` + stubURL + `"
api_key_env = "CLIAUTOMATIONS_RESUME_FAIL_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	spec := automation.Spec{
		ID:      "resume-will-fail",
		Name:    "resume-will-fail",
		Enabled: true,
		Steps:   []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}},
	}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}

	// First fire: RunOnce against the failing provider, producing a real
	// RunFailed row with a durably-saved session.
	if err := runRunCommand([]string{"--workspace", root, "resume-will-fail"}); err == nil {
		t.Fatal("runRunCommand against a genuinely failing provider: got nil error, want the run's failure surfaced")
	}

	svc, _, cleanup, err := buildService(root, "")
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	runs := svc.Runs("resume-will-fail", 1)
	cleanup()
	if len(runs) != 1 {
		t.Fatalf("Runs after the first failed fire = %d, want exactly 1", len(runs))
	}
	runID := runs[0].ID

	// Resume the same run against the SAME still-failing provider: the
	// step fails again for real, exercising resume_cmd.go's own
	// runFailed(run.State) branch.
	stdout, _ := captureOutput(t, func() {
		err := runResumeCommand([]string{"--workspace", root, runID})
		if err == nil {
			t.Fatal("runResumeCommand against a genuinely failing provider: got nil error, want the resumed run's failure surfaced")
		}
	})
	if !strings.Contains(stdout, "state=failed") {
		t.Fatalf("runResumeCommand stdout = %q, want it to have printed the failed run result before returning the error", stdout)
	}
}
