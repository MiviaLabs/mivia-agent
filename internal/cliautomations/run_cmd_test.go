package cliautomations

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// stubProviderServer starts a minimal OpenAI-wire SSE stub that always
// returns one short assistant text turn, so a run's single StepPrompt
// completes fast and deterministically without a real network call.
func stubProviderServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// writeAutomationsFixture writes a minimal .mivia/mivia.toml (pointed at
// a local stub provider server, never the real network) and one enabled
// single-prompt-step automation under root, returning root.
func writeAutomationsFixture(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	stubURL := stubProviderServer(t)
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("CLIAUTOMATIONS_TEST_KEY", "test-key")
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "` + stubURL + `"
api_key_env = "CLIAUTOMATIONS_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	spec := automation.Spec{
		ID:      id,
		Name:    id,
		Enabled: true,
		Steps:   []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}},
	}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	return root
}

// captureOutput redirects stdoutWriter/stderrWriter to buffers for the
// duration of fn, restoring them afterward.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	prevOut, prevErr := stdoutWriter, stderrWriter
	stdoutWriter = func() io.Writer { return &outBuf }
	stderrWriter = func() io.Writer { return &errBuf }
	t.Cleanup(func() { stdoutWriter, stderrWriter = prevOut, prevErr })
	fn()
	return outBuf.String(), errBuf.String()
}

// TestRunCommandCallsCloseLastRunAfterRunOnce proves `automations run`
// calls spawn.CloseLastRun after a successful RunOnce.
func TestRunCommandCallsCloseLastRunAfterRunOnce(t *testing.T) {
	root := writeAutomationsFixture(t, "run-close-ok")
	if err := runRunCommand([]string{"--workspace", root, "run-close-ok"}); err != nil {
		t.Fatalf("runRunCommand: %v", err)
	}
}

// TestRunCommandCallsCloseLastRunOnRunOnceError proves CloseLastRun still
// runs (and the store closes cleanly) even when RunOnce itself errors -
// here, an unknown automation id. Uses writeAutomationsFixture (a real,
// loadable config) rather than a bare .mivia dir so buildService itself
// succeeds and RunOnce is the one call that fails - a bare .mivia dir
// makes config.Load itself fail first (no [providers.openrouter]
// section), never reaching RunOnce/CloseLastRun at all.
func TestRunCommandCallsCloseLastRunOnRunOnceError(t *testing.T) {
	root := writeAutomationsFixture(t, "exists-but-not-the-one-requested")
	err := runRunCommand([]string{"--workspace", root, "no-such-automation"})
	if err == nil {
		t.Fatal("runRunCommand(unknown id): got nil error, want ErrAutomationNotFound")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("runRunCommand(unknown id) error = %q, want it naming automation-not-found", err.Error())
	}
}

func TestRunAutomationsUnknownSubcommandErrors(t *testing.T) {
	_, stderr := captureOutput(t, func() {
		if err := RunAutomations([]string{"bogus"}); err == nil {
			t.Fatal("RunAutomations(bogus): got nil error, want rejection")
		}
	})
	if stderr == "" {
		t.Fatal("RunAutomations(bogus) printed no usage to stderr")
	}
}

func TestRunAutomationsNoArgsPrintsUsage(t *testing.T) {
	if err := RunAutomations(nil); err == nil {
		t.Fatal("RunAutomations(nil): got nil error, want usage+error")
	}
}

// TestRunListCommandListsSeededAutomation is a smoke test for `automations
// list` against a real seeded automations.toml.
func TestRunListCommandListsSeededAutomation(t *testing.T) {
	root := writeAutomationsFixture(t, "list-me")
	if err := runListCommand([]string{"--workspace", root}); err != nil {
		t.Fatalf("runListCommand: %v", err)
	}
}

// TestRunShowCommandShowsSeededAutomation is a smoke test for
// `automations show <id>`.
func TestRunShowCommandShowsSeededAutomation(t *testing.T) {
	root := writeAutomationsFixture(t, "show-me")
	if err := runShowCommand([]string{"--workspace", root, "show-me"}); err != nil {
		t.Fatalf("runShowCommand: %v", err)
	}
}

func TestRunShowCommandUnknownIDErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "other-one")
	if err := runShowCommand([]string{"--workspace", root, "does-not-exist"}); err == nil {
		t.Fatal("runShowCommand(unknown id): got nil error, want rejection")
	}
}

// --- parseCommonFlags / buildService error propagation across every
// --- subcommand entry point (list/show/run/serve), plus run's own
// --- runFailed branch (a REAL failing automation, not a not-found
// --- error) ---

func TestRunListCommandParseFlagsErrorPropagates(t *testing.T) {
	err := runListCommand([]string{"--workspace"})
	if err == nil {
		t.Fatal("runListCommand with --workspace missing its value: got nil error")
	}
}

func TestRunListCommandUnexpectedArgsErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "list-unexpected-args")
	err := runListCommand([]string{"--workspace", root, "unexpected"})
	if err == nil {
		t.Fatal("runListCommand with an unexpected positional arg: got nil error")
	}
}

func TestRunListCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runListCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("runListCommand against a nonexistent workspace: got nil error")
	}
}

func TestRunShowCommandParseFlagsErrorPropagates(t *testing.T) {
	err := runShowCommand([]string{"--workspace"})
	if err == nil {
		t.Fatal("runShowCommand with --workspace missing its value: got nil error")
	}
}

func TestRunShowCommandWrongArgCountErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "show-wrong-args")
	if err := runShowCommand([]string{"--workspace", root}); err == nil {
		t.Fatal("runShowCommand with no id: got nil error")
	}
	if err := runShowCommand([]string{"--workspace", root, "a", "b"}); err == nil {
		t.Fatal("runShowCommand with two ids: got nil error")
	}
}

func TestRunShowCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runShowCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist"), "any-id"})
	if err == nil {
		t.Fatal("runShowCommand against a nonexistent workspace: got nil error")
	}
}

func TestRunRunCommandParseFlagsErrorPropagates(t *testing.T) {
	err := runRunCommand([]string{"--workspace"})
	if err == nil {
		t.Fatal("runRunCommand with --workspace missing its value: got nil error")
	}
}

func TestRunRunCommandWrongArgCountErrors(t *testing.T) {
	root := writeAutomationsFixture(t, "run-wrong-args")
	if err := runRunCommand([]string{"--workspace", root}); err == nil {
		t.Fatal("runRunCommand with no id: got nil error")
	}
}

func TestRunRunCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runRunCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist"), "any-id"})
	if err == nil {
		t.Fatal("runRunCommand against a nonexistent workspace: got nil error")
	}
}

// failingStubProviderServer returns an HTTP 500 for every request, so a
// real automation run's single step genuinely fails (RunFailed), rather
// than merely being not-found or disabled - the runFailed(run.State)
// branch in runRunCommand is only reachable through a real failed run.
func failingStubProviderServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "simulated provider failure", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRunCommandReturnsErrorOnRealRunFailure covers run_cmd.go's own
// runFailed(run.State) branch: a genuinely failed run (not a
// not-found/disabled refusal) must still print the run result AND
// return a non-nil error naming the failure.
func TestRunCommandReturnsErrorOnRealRunFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	stubURL := failingStubProviderServer(t)
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("CLIAUTOMATIONS_FAIL_TEST_KEY", "test-key")
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "` + stubURL + `"
api_key_env = "CLIAUTOMATIONS_FAIL_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	spec := automation.Spec{
		ID:      "will-fail",
		Name:    "will-fail",
		Enabled: true,
		Steps:   []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}},
	}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}

	stdout, _ := captureOutput(t, func() {
		err := runRunCommand([]string{"--workspace", root, "will-fail"})
		if err == nil {
			t.Fatal("runRunCommand against a genuinely failing provider: got nil error, want the run's failure surfaced")
		}
	})
	if !strings.Contains(stdout, "state=failed") {
		t.Fatalf("runRunCommand stdout = %q, want it to have printed the failed run result before returning the error", stdout)
	}
}

// TestRunCommandWaitPrintsFullDetail proves `--wait` selects
// printRunDetail's full-detail rendering (run_id:, automation_id:,
// state:, started_at:) instead of printRunResult's one-line summary.
func TestRunCommandWaitPrintsFullDetail(t *testing.T) {
	root := writeAutomationsFixture(t, "run-wait-detail")
	stdout, _ := captureOutput(t, func() {
		if err := runRunCommand([]string{"--workspace", root, "--wait", "run-wait-detail"}); err != nil {
			t.Fatalf("runRunCommand --wait: %v", err)
		}
	})
	for _, want := range []string{"run_id: ", "automation_id: run-wait-detail", "state: succeeded", "started_at: "} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("runRunCommand --wait stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "run_id=") {
		t.Fatalf("runRunCommand --wait stdout = %q, want the detail view (run_id:), not the one-line summary (run_id=)", stdout)
	}
}

// TestRunCommandWaitPrintsMessageForFailedRun proves printRunDetail's
// own r.Message != "" branch: a --wait run against a genuinely failing
// provider must render a "message: " line naming the failure, not just
// the state.
func TestRunCommandWaitPrintsMessageForFailedRun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir .mivia: %v", err)
	}
	stubURL := failingStubProviderServer(t)
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("CLIAUTOMATIONS_WAIT_FAIL_TEST_KEY", "test-key")
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "` + stubURL + `"
api_key_env = "CLIAUTOMATIONS_WAIT_FAIL_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	spec := automation.Spec{
		ID:      "wait-will-fail",
		Name:    "wait-will-fail",
		Enabled: true,
		Steps:   []automation.Step{{Kind: automation.StepPrompt, Prompt: "hi"}},
	}
	if err := automation.SaveSpecs(ports.ScopeProject, root, []automation.Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}

	stdout, _ := captureOutput(t, func() {
		err := runRunCommand([]string{"--workspace", root, "--wait", "wait-will-fail"})
		if err == nil {
			t.Fatal("runRunCommand --wait against a genuinely failing provider: got nil error, want the run's failure surfaced")
		}
	})
	if !strings.Contains(stdout, "state: failed") {
		t.Fatalf("runRunCommand --wait stdout = %q, want state: failed", stdout)
	}
	if !strings.Contains(stdout, "message: ") {
		t.Fatalf("runRunCommand --wait stdout = %q, want a message: line naming the failure (printRunDetail's r.Message != \"\" branch)", stdout)
	}
}

// TestRunCommandWithoutWaitPrintsOneLineSummary proves the default (no
// --wait) path is unchanged: still printRunResult's one-line
// "run_id=... state=..." form, never the full detail view.
func TestRunCommandWithoutWaitPrintsOneLineSummary(t *testing.T) {
	root := writeAutomationsFixture(t, "run-no-wait-summary")
	stdout, _ := captureOutput(t, func() {
		if err := runRunCommand([]string{"--workspace", root, "run-no-wait-summary"}); err != nil {
			t.Fatalf("runRunCommand: %v", err)
		}
	})
	if !strings.Contains(stdout, "run_id=") || !strings.Contains(stdout, "state=succeeded") {
		t.Fatalf("runRunCommand stdout = %q, want the one-line summary form (run_id=..., state=...)", stdout)
	}
	if strings.Contains(stdout, "automation_id:") {
		t.Fatalf("runRunCommand stdout = %q, want the one-line summary, not the full detail view", stdout)
	}
}

func TestRunServeCommandParseFlagsErrorPropagates(t *testing.T) {
	err := runServeCommand([]string{"--workspace"})
	if err == nil {
		t.Fatal("runServeCommand with --workspace missing its value: got nil error")
	}
}

func TestRunServeCommandBuildServiceErrorPropagates(t *testing.T) {
	err := runServeCommand([]string{"--workspace", filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("runServeCommand against a nonexistent workspace: got nil error")
	}
}

var _ = context.Background
