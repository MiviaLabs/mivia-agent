package cliautomations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunAutomationsDispatchesEachSubcommandThroughTheSwitch drives all
// four RunAutomations subcommands THROUGH the real dispatch switch
// (RunAutomations([]string{"list", ...})), unlike run_cmd_test.go's
// existing tests, which call runListCommand/runShowCommand/
// runRunCommand/runServeCommand directly and so never exercise
// RunAutomations's own switch-case lines.
func TestRunAutomationsDispatchesEachSubcommandThroughTheSwitch(t *testing.T) {
	root := writeAutomationsFixture(t, "dispatch-me")

	if err := RunAutomations([]string{"list", "--workspace", root}); err != nil {
		t.Fatalf("RunAutomations(list): %v", err)
	}
	if err := RunAutomations([]string{"show", "--workspace", root, "dispatch-me"}); err != nil {
		t.Fatalf("RunAutomations(show): %v", err)
	}
	if err := RunAutomations([]string{"run", "--workspace", root, "dispatch-me"}); err != nil {
		t.Fatalf("RunAutomations(run): %v", err)
	}
}

// TestRunAutomationsServeDispatchesThroughTheSwitch proves the "serve"
// case reaches runServeCommand - substituting serveSignalContext with
// an already-cancelled context (the same technique
// TestServeCommandExitsCleanlyOnSignal uses) so this returns promptly
// without needing a real OS signal or blocking on the scheduler loop.
func TestRunAutomationsServeDispatchesThroughTheSwitch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "http://127.0.0.1:0"
api_key_env = "CLIAUTOMATIONS_DISPATCH_SERVE_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	t.Setenv("CLIAUTOMATIONS_DISPATCH_SERVE_KEY", "test-key")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: Serve returns immediately
	prev := serveSignalContext
	serveSignalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { serveSignalContext = prev })

	errCh := make(chan error, 1)
	go func() { errCh <- RunAutomations([]string{"serve", "--workspace", root}) }()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunAutomations(serve) via dispatch = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAutomations(serve) did not return within 5s")
	}
}

// TestRunAutomationsDispatchesRunsAndResumeThroughTheSwitch drives the
// "runs" and "resume" RunAutomations subcommands THROUGH the real
// dispatch switch (unlike runs_cmd_test.go/resume_cmd_test.go, which
// call runRunsCommand/runResumeCommand directly and so never exercise
// RunAutomations's own switch-case lines for these two subcommands).
func TestRunAutomationsDispatchesRunsAndResumeThroughTheSwitch(t *testing.T) {
	root := writeAutomationsFixture(t, "dispatch-runs-resume")

	if err := RunAutomations([]string{"runs", "--workspace", root}); err != nil {
		t.Fatalf("RunAutomations(runs): %v", err)
	}
	// "resume" with an unknown run id: the switch case itself is what
	// this test proves reachable, not resume's own success path (that
	// is resume_cmd_test.go's job) - any error naming ErrRunNotFound
	// proves runResumeCommand was actually invoked through the switch.
	err := RunAutomations([]string{"resume", "--workspace", root, "no-such-run"})
	if err == nil {
		t.Fatal("RunAutomations(resume, unknown run id): got nil error, want rejection")
	}
}

// TestAutomationsUsageTextListsAllSixSubcommands pins the D14 usage
// text: automationsUsageText() must document every one of the six
// `automations` subcommands (list, show, run, runs, resume, serve),
// each with its exact documented flag set.
func TestAutomationsUsageTextListsAllSixSubcommands(t *testing.T) {
	text := automationsUsageText()
	for _, want := range []string{
		"mivia automations list [--workspace dir] [--config path]",
		"mivia automations show <id> [--workspace dir] [--config path]",
		"mivia automations run <id> [--wait] [--workspace dir] [--config path]",
		"mivia automations runs [--automation <id>] [--limit <n>] [--workspace dir] [--config path]",
		"mivia automations resume <run-id> [--workspace dir] [--config path]",
		"mivia automations serve [--workspace dir] [--config path]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("automationsUsageText() missing %q:\n%s", want, text)
		}
	}
}

func TestRunAutomationsUnknownSubcommandErrorsThroughDispatch(t *testing.T) {
	err := RunAutomations([]string{"bogus-subcommand"})
	if err == nil {
		t.Fatal("RunAutomations(bogus-subcommand): got nil error, want rejection")
	}
}

// --- resolveWorkspaceAndConfig / automationConfigPath error branches ---

func TestResolveWorkspaceAndConfigWorkspaceOpenError(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist-at-all")
	_, _, err := resolveWorkspaceAndConfig(nonExistent, "")
	if err == nil {
		t.Fatal("resolveWorkspaceAndConfig against a nonexistent workspace: got nil error")
	}
}

func TestResolveWorkspaceAndConfigLoadError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	badConfig := filepath.Join(root, ".mivia", "bad.toml")
	if err := os.WriteFile(badConfig, []byte("not = [valid toml"), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	_, _, err := resolveWorkspaceAndConfig(root, badConfig)
	if err == nil {
		t.Fatal("resolveWorkspaceAndConfig against malformed config: got nil error")
	}
}

func TestAutomationConfigPathExplicitWins(t *testing.T) {
	got := automationConfigPath("/some/root", "/explicit/path.toml")
	if got != "/explicit/path.toml" {
		t.Fatalf("automationConfigPath with an explicit path = %q, want it returned verbatim", got)
	}
}

func TestAutomationConfigPathFallsBackToWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := filepath.Join(root, ".mivia", "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte("[provider]\nname=\"openrouter\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	got := automationConfigPath(root, "")
	if got != cfgPath {
		t.Fatalf("automationConfigPath(root, \"\") = %q, want %q", got, cfgPath)
	}
}

func TestAutomationConfigPathNoWorkspaceFileReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	got := automationConfigPath(root, "")
	if got != "" {
		t.Fatalf("automationConfigPath with no workspace config = %q, want empty (falls through to config.Load's own defaults)", got)
	}
}

// TestResolveWorkspaceAndConfigDefaultsEmptyWorkspaceRootToCwd covers
// resolveWorkspaceAndConfig's own empty-workspaceRoot fallback
// (workspaceRoot = "."): calling it with "" must resolve the SAME root
// workspace.Open(".") would from the process's current working
// directory, not fail or silently use some other default. Chdirs into a
// fresh fixture directory (restored via t.Cleanup) so the process cwd
// has a real, loadable config - this repo's own cwd during `go test`
// has no [providers.openrouter] section, so the empty-root path would
// otherwise fail at config.Load for an unrelated reason before ever
// reaching the assertion this test targets.
func TestResolveWorkspaceAndConfigDefaultsEmptyWorkspaceRootToCwd(t *testing.T) {
	root := writeAutomationsFixture(t, "cwd-default-fixture")
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("os.Chdir(%q): %v", root, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	gotRoot, _, err := resolveWorkspaceAndConfig("", "")
	if err != nil {
		t.Fatalf("resolveWorkspaceAndConfig(\"\", \"\"): %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		wantRoot = root
	}
	if gotRoot != wantRoot {
		t.Fatalf("resolveWorkspaceAndConfig(\"\", \"\") root = %q, want %q (workspace.Open(\".\") from cwd)", gotRoot, wantRoot)
	}
}

// --- buildService error branches ---

func TestBuildServiceOpenAutomationStoreError(t *testing.T) {
	root := writeAutomationsFixture(t, "store-error-fixture")
	// Make the .mivia directory unwritable AFTER a real, loadable config
	// already exists under it, so resolveWorkspaceAndConfig succeeds for
	// real and buildService reaches its own openAutomationStore call -
	// which then fails to create automations.db under the now-read-only
	// directory. Making .mivia read-only BEFORE writing a config (the
	// prior version of this test) instead made config.Load itself fail
	// first (no [providers.openrouter] section ever got written), never
	// reaching openAutomationStore at all.
	miviaDir := filepath.Join(root, ".mivia")
	if err := os.Chmod(miviaDir, 0o500); err != nil {
		t.Fatalf("chmod read-only .mivia: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(miviaDir, 0o755) })

	_, _, _, err := buildService(root, "")
	if err == nil {
		t.Fatal("buildService against an unwritable .mivia dir: got nil error, want openAutomationStore's failure")
	}
}

// --- parseCommonFlags / flagValueSeam branches ---

func TestParseCommonFlagsEqualsForm(t *testing.T) {
	workspaceRoot, configPath, rest, err := parseCommonFlags([]string{"--workspace=/a/b", "--config=/c/d.toml", "positional"})
	if err != nil {
		t.Fatalf("parseCommonFlags: %v", err)
	}
	if workspaceRoot != "/a/b" {
		t.Fatalf("workspaceRoot = %q, want /a/b (= form)", workspaceRoot)
	}
	if configPath != "/c/d.toml" {
		t.Fatalf("configPath = %q, want /c/d.toml (= form)", configPath)
	}
	if len(rest) != 1 || rest[0] != "positional" {
		t.Fatalf("rest = %v, want [positional]", rest)
	}
}

func TestFlagValueSeamMissingValueAfterFlagErrors(t *testing.T) {
	_, _, _, err := flagValueSeam([]string{"--workspace"}, "--workspace")
	if err == nil {
		t.Fatal("flagValueSeam with a flag and no following value: got nil error")
	}
}

func TestFlagValueSeamDashPrefixedValueErrors(t *testing.T) {
	_, _, _, err := flagValueSeam([]string{"--workspace", "--other-flag"}, "--workspace")
	if err == nil {
		t.Fatal("flagValueSeam with a dash-prefixed value after the flag: got nil error")
	}
}

func TestParseCommonFlagsConfigErrorPropagates(t *testing.T) {
	_, _, _, err := parseCommonFlags([]string{"--workspace", "/a", "--config"})
	if err == nil {
		t.Fatal("parseCommonFlags with --config missing its value: got nil error")
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("parseCommonFlags error = %q, want it naming --config", err.Error())
	}
}
