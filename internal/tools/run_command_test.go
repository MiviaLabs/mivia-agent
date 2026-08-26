package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestRunAllowlist(t *testing.T) {
	_, reg := setupWS(t)
	ctx := context.Background()
	// git prints "git: 'hi' is not a git command" (argv visible in the result
	// header), and exits non-zero - it stands in for echo on Windows where
	// echo/false are shell builtins, not executables.
	out, err := reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["git","hi"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("out=%q", out)
	}
	_, err = reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["sudo","echo","hi"]}`))
	if err == nil {
		t.Fatal("expected allowlist reject for sudo")
	}
	_, err = reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["./hello"]}`))
	if err == nil {
		t.Fatal("expected path binary reject")
	}
}

func TestRunCommandShowsArgumentsByDefault(t *testing.T) {
	// Default: operator/model see argv (redaction opt-in only).
	SetRedactToolArgs(false)
	t.Cleanup(func() { SetRedactToolArgs(false) })
	_, reg := setupWS(t)
	marker := "visible-arg-xyz"
	// git prints its unknown-command error (which echoes the argument) and
	// exits non-zero; it stands in for false on Windows where false is a
	// shell builtin, not an executable.
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["git",%q]}`, marker)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("expected argv in result by default, got %q", out)
	}
	if strings.Contains(out, "arguments redacted") {
		t.Fatalf("redaction must be off by default: %q", out)
	}
}

func TestRunCommandRedactsArgumentsWhenEnabled(t *testing.T) {
	SetRedactToolArgs(true)
	t.Cleanup(func() { SetRedactToolArgs(false) })
	_, reg := setupWS(t)
	secret := "person@example.com"
	// git rev-parse fails without echoing its argument ("fatal: not a git
	// repository"), so the arg-redaction mode is what keeps the secret out of
	// the output body - the same property false had on Unix.
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["git","rev-parse","--verify",%q]}`, secret)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("raw argument leaked when redaction on: %q", out)
	}
	if !strings.Contains(out, "arguments redacted") {
		t.Fatalf("missing redaction marker: %q", out)
	}
}

func TestFormatArgvAndEnvParse(t *testing.T) {
	if got := FormatArgv([]string{"git", "status"}); got != "git status" {
		t.Fatalf("simple argv=%q", got)
	}
	if got := FormatArgv([]string{"echo", "a b"}); !strings.Contains(got, `"`) {
		t.Fatalf("expected quoting: %q", got)
	}
	t.Setenv(EnvRedactToolArgs, "1")
	if !ApplyRedactToolArgsEnv() || !RedactToolArgs() {
		t.Fatal("env true")
	}
	t.Setenv(EnvRedactToolArgs, "0")
	if !ApplyRedactToolArgsEnv() || RedactToolArgs() {
		t.Fatal("env false")
	}
	SetRedactToolArgs(false)
}

func TestRunCommandCapturesFailure(t *testing.T) {
	_, reg := setupWS(t)
	// git exits 1 for an unknown subcommand but is allowlisted; the exit
	// status must surface in the result body.
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["git"]}`))
	// Execute returns nil error so model sees stdout/stderr; check status in body.
	if err != nil {
		t.Fatalf("unexpected exec error: %v", err)
	}
	if !strings.Contains(out, "exit=") {
		t.Fatalf("out=%q", out)
	}
}

func TestRunCommandTimeoutKillsUnixProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process groups are not available on Windows")
	}
	ws, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 1})
	marker := filepath.Join(ws.Abs, "child-survived")
	args := map[string]any{
		"argv": []string{"sh", "-c", `sleep 3; touch "$1"`, "sh", marker},
	}

	started := time.Now()
	out, err := reg.Execute(context.Background(), "run_command", mustJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit=timeout") {
		t.Fatalf("expected timeout, got %q", out)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("timed-out child survived process-group cancellation")
		}
		select {
		case <-deadline.C:
			return
		default:
			runtime.Gosched()
		}
	}
}

func TestRunCommandCapabilityAdvertisesProcessBudget(t *testing.T) {
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"true"}, RunTimeoutSec: 300})
	cap := reg.Capability("run_command", json.RawMessage(`{"argv":["true"]}`))
	if cap.Timeout != 300*time.Second {
		t.Fatalf("run_command capability timeout=%s want 300s so agent loop can extend past default 60s", cap.Timeout)
	}
	if cap.Class != ExecutionExternal {
		t.Fatalf("class=%v want ExecutionExternal", cap.Class)
	}
}

func TestRunCommandHonorsParentDeadlineWithoutHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 30})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["sh","-c","sleep 5"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("parent deadline hang: %s", elapsed)
	}
	// Parent deadline → exit=timeout; parent cancel → exit=canceled. Never silent hang.
	if !strings.Contains(out, "exit=timeout") && !strings.Contains(out, "exit=canceled") {
		t.Fatalf("expected exit=timeout or exit=canceled in body, got %q", out)
	}
}

// Retargeted from the deleted compiled-in isAllowedEnvVar onto the live path:
// the same expectations must hold for a tool configured from the example
// config, or the policy silently changed when the list moved to TOML.
func TestExampleEnvConfigAllowsSafeVarsAndBlocksSecrets(t *testing.T) {
	tests := []struct {
		key   string
		allow bool
	}{
		// Critical POSIX vars - must be allowed
		{"PATH", true},
		{"HOME", true},
		{"USER", true},
		{"TMPDIR", true},
		{"TERM", true},
		{"LANG", true},
		{"PWD", true},
		// LC_* prefix
		{"LC_ALL", true},
		{"LC_MESSAGES", true},
		// XDG_* prefix
		{"XDG_CONFIG_HOME", true},
		{"XDG_DATA_DIRS", true},
		// Known non-secret GIT_* vars
		{"GIT_PAGER", true},
		{"GIT_EDITOR", true},
		// Known secrets - must be blocked
		{"API_KEY", false},
		{"SECRET", false},
		{"TOKEN", false},
		{"PASSWORD", false},
		{"MY_API_KEY", false},
		{"DATABASE_PASSWORD", false},
		{"GITHUB_TOKEN", false},
		{"AWS_SECRET_ACCESS_KEY", false},
		{"SLACK_TOKEN", false},
		{"NPM_TOKEN", false},
		// Unknown vars - default blocked
		{"MY_CUSTOM_VAR", false},
		{"FOOBAR", false},
		{"PROJECT_HOME", false},
	}
	exact, prefixes, _ := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}
	for _, tt := range tests {
		got := containsEnv(tool.filterEnv([]string{tt.key + "=x"}), tt.key)
		if got != tt.allow {
			t.Errorf("filterEnv(%q) allowed=%v, want %v", tt.key, got, tt.allow)
		}
	}
}

func TestFilterEnv_DropsSecretsKeepsSafe(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/root",
		"USER=root",
		"LANG=en_US.UTF-8",
		"SECRET=supersekret",
		"DB_PASSWORD=hunter2",
		"API_KEY=sk-abc123",
		"GITHUB_TOKEN=ghp_def456",
	}
	// Build the tool with default allowlists (no user overrides).
	exact, prefixes, _ := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes, envKeywordBlock: testEnvKeywordBlock}
	filtered := tool.filterEnv(env)
	if len(filtered) != 4 {
		t.Fatalf("expected 4 safe vars, got %d: %v", len(filtered), filtered)
	}
	// Verify all 4 are from the exact or prefix allowlists (not the deprecated isAllowedEnvVar).
	for _, e := range filtered {
		key, _, _ := strings.Cut(e, "=")
		uk := strings.ToUpper(key)
		if exact[uk] {
			continue
		}
		matched := false
		for _, p := range prefixes {
			if strings.HasPrefix(uk, p) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("filterEnv leaked disallowed var: %s (not in exact or prefix allowlists)", key)
		}
	}
}

// TestFilterEnvEmptyAllowlistReturnsNonNil pins the fail-closed contract of
// the shared environment filter: a zero-valued tool - the default
// configuration, where [tools].env_allowlist is unset - must return an EMPTY,
// NON-NIL slice. Assigning nil to exec.Cmd.Env makes os/exec inherit the
// parent's FULL environment (os/exec.Cmd.Env documentation), leaking operator
// secrets to allowlisted child programs; the empty non-nil slice gives the
// child NO environment, matching env.go's documented contract "With it unset,
// child processes inherit no environment". The len==0 assertion is the
// negative path proving fail-closed even when the child env is empty.
func TestFilterEnvEmptyAllowlistReturnsNonNil(t *testing.T) {
	tool := &runCommandTool{}
	got := tool.filterEnv([]string{"PATH=/usr/bin:/bin", "SECRET=supersekret", "HOME=/root"})
	if got == nil {
		t.Fatal("empty allowlist must yield an empty NON-NIL slice; nil cmd.Env inherits the parent's full environment")
	}
	if len(got) != 0 {
		t.Fatalf("empty allowlist must pass nothing through, got %v", got)
	}
}

// useRedactionPolicy installs a process-wide policy for the duration of a test.
// Redaction is configuration, so every assertion that a credential disappears
// has to say which configuration made it disappear.
func useRedactionPolicy(t *testing.T, patterns []string) {
	t.Helper()
	policy, err := redact.Compile(patterns, nil, "")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(nil) })
}

// credentialPattern is the recommended value-prefix rule from
// .mivia/mivia.toml.example. It used to be compiled into run.go as scrubSecrets.
const credentialPattern = `(?:sk-ant-|sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9._~-]+`

// Retargets TestScrubSecrets_RedactsKeyPatterns: the same prefixes are still
// redacted, but only because the workspace configured them.
func TestRunCommandRedactsOutputWithConfiguredPolicy(t *testing.T) {
	useRedactionPolicy(t, []string{credentialPattern})
	_, reg := setupWS(t)
	for _, secret := range []string{"sk-abc123XYZ", "ghp_abc123def456", "github_pat_abc123"} {
		out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["git",%q]}`, secret)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, secret) {
			t.Errorf("configured policy did not redact %q from run_command output: %q", secret, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("expected placeholder in output for %q: %q", secret, out)
		}
	}
}

// The argv line of the result header goes through the same policy as the body.
func TestRunCommandRedactsArgvHeaderWithConfiguredPolicy(t *testing.T) {
	SetRedactToolArgs(false)
	t.Cleanup(func() { SetRedactToolArgs(false) })
	useRedactionPolicy(t, []string{credentialPattern})
	_, reg := setupWS(t)
	const secret = "ghp_headerleak123"
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["git",%q]}`, secret)))
	if err != nil {
		t.Fatal(err)
	}
	command, _, _ := strings.Cut(out, "\n")
	if strings.Contains(command, secret) {
		t.Fatalf("credential leaked in argv header: %q", command)
	}
	if !strings.Contains(command, "[redacted]") {
		t.Fatalf("expected placeholder in argv header: %q", command)
	}
}

// The fail-open posture, tested. An unconfigured workspace redacts nothing -
// not in the argv header, and not in the model-visible output body.
func TestRunCommandWithNoPolicyRedactsNothing(t *testing.T) {
	SetRedactToolArgs(false)
	t.Cleanup(func() { SetRedactToolArgs(false) })
	redact.SetPolicy(nil)
	t.Cleanup(func() { redact.SetPolicy(nil) })
	_, reg := setupWS(t)
	// Assembled rather than written literally so the repo secret scanner
	// does not flag an obviously fake fixture.
	secret := "sk-" + "ant-unconfigured-workspace-token"
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["git",%q]}`, secret)))
	if err != nil {
		t.Fatal(err)
	}
	command, body, _ := strings.Cut(out, "\n")
	if !strings.Contains(command, secret) {
		t.Fatalf("argv header redacted with no policy configured: %q", command)
	}
	if !strings.Contains(body, secret) {
		t.Fatalf("output body redacted with no policy configured: %q", body)
	}
}

func TestParseTruthyEnv(t *testing.T) {
	tests := []struct {
		val string
		on  bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"", false},
		{"maybe", false},
	}
	for _, tt := range tests {
		got := parseTruthyEnv(tt.val)
		if got != tt.on {
			t.Errorf("parseTruthyEnv(%q) = %v, want %v", tt.val, got, tt.on)
		}
	}
}

func TestRunCommandParentCancelReportsCanceled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 30})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled parent - must not hang; status exit=canceled
	start := time.Now()
	out, err := reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["sh","-c","sleep 10"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel hang: %s", elapsed)
	}
	if !strings.Contains(out, "exit=canceled") {
		t.Fatalf("expected exit=canceled, got %q", out)
	}
}

// TestRunCommandRegisteredWithBuiltinAllowlistByDefault proves run_command
// is open by default: with no [tools] run_allowlist configured, it is still
// registered and can run a program from config.DefaultRunAllowlist (e.g.
// "echo"). See config.DefaultRunAllowlist's doc comment for what is (and is
// not) included by default.
func TestRunCommandRegisteredWithBuiltinAllowlistByDefault(t *testing.T) {
	ws := setupTestWSRun(t)
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	if _, ok := reg.Get(RunCommandToolName); !ok {
		t.Fatal("run_command should be registered by default (built-in allowlist)")
	}
	out, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(`{"argv":["echo","hi"]}`))
	if err != nil {
		t.Fatalf("Execute(echo) error = %v, want success from the built-in allowlist", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("out=%q, want it to contain echo's output", out)
	}
}

// TestRunCommandBuiltinAllowlistExcludesShellsAndMutatingTools proves the
// built-in default deliberately omits shells (unrestricted execution) and
// file-mutating programs (run_command is not gated by the write-path
// blocklist, so a mutating program here would bypass it entirely).
func TestRunCommandBuiltinAllowlistExcludesShellsAndMutatingTools(t *testing.T) {
	ws := setupTestWSRun(t)
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	for _, program := range []string{"sh", "bash", "rm", "cp", "mv", "chmod", "find", "curl", "docker"} {
		_, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(fmt.Sprintf(`{"argv":[%q]}`, program)))
		if err == nil {
			t.Errorf("Execute(%s) error = nil, want an allowlist rejection (not in the built-in default)", program)
		}
	}
}

// TestRunCommandAllowlistOnlyReplacesBuiltin proves run_allowlist_only
// replaces config.DefaultRunAllowlist entirely rather than extending it: a
// program from the built-in default that is not in the configured
// run_allowlist_only is refused.
func TestRunCommandAllowlistOnlyReplacesBuiltin(t *testing.T) {
	ws := setupTestWSRun(t)
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, RunAllowlistOnly: []string{"git"}})
	if _, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(`{"argv":["echo","hi"]}`)); err == nil {
		t.Error("Execute(echo) error = nil, want rejection: run_allowlist_only replaces the built-in default")
	}
	if _, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(`{"argv":["git","--version"]}`)); err != nil {
		t.Errorf("Execute(git) error = %v, want success: git is in run_allowlist_only", err)
	}
}

// TestRunCommandRunBlocklistRemovesBuiltinEntry proves run_blocklist can
// remove a program from the built-in default, not only from configured
// run_allowlist entries.
func TestRunCommandRunBlocklistRemovesBuiltinEntry(t *testing.T) {
	ws := setupTestWSRun(t)
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, RunBlocklist: []string{"echo"}})
	if _, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(`{"argv":["echo","hi"]}`)); err == nil {
		t.Error("Execute(echo) error = nil, want rejection: echo is run_blocklist-ed")
	}
	if _, err := reg.Execute(context.Background(), RunCommandToolName, json.RawMessage(`{"argv":["git","--version"]}`)); err != nil {
		t.Errorf("Execute(git) error = %v, want success: only echo was blocklisted", err)
	}
}

// TestRunCommandBuildCommandError covers Execute's command-build error path:
// a tool whose allowlisted program cannot be resolved on PATH fails before any
// process is started. The tool is constructed directly so the allowlist can
// name a binary that is guaranteed not to exist.
func TestRunCommandBuildCommandError(t *testing.T) {
	ws := setupTestWSRun(t)
	tool := &runCommandTool{
		ws:         ws,
		allowlist:  []string{"definitely-not-a-real-binary-on-any-path"},
		timeoutSec: 30,
	}
	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"argv":["definitely-not-a-real-binary-on-any-path"]}`))
	if err == nil {
		t.Fatal("expected error when the allowlisted binary cannot be resolved")
	}
	if !strings.Contains(err.Error(), "program not found on PATH") {
		t.Fatalf("expected not-found error, got: %v", err)
	}
}

func setupTestWSRun(t *testing.T) *workspace.Root {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestRunCommandStdoutStderrSeparate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell path")
	}
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}})
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["sh","-c","echo out; echo err >&2"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stdout:") {
		t.Errorf("missing stdout section: %q", out)
	}
	if !strings.Contains(out, "stderr:") {
		t.Errorf("missing stderr section: %q", out)
	}
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("expected output content: %q", out)
	}
}

func TestRunCommandStderrOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell path")
	}
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}})
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["sh","-c","echo erronly >&2"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stderr:") {
		t.Errorf("missing stderr section: %q", out)
	}
	if strings.Contains(out, "stdout:") {
		t.Errorf("unexpected stdout section when only stderr was produced: %q", out)
	}
}

func TestRunCommandStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cat path")
	}
	_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"cat"}})
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["cat"],"stdin":"hello from stdin"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello from stdin") {
		t.Errorf("expected stdin content in output: %q", out)
	}
}

func TestRunCommandCwdRelative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path handling")
	}
	ws, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}})
	sub := filepath.Join(ws.Abs, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["sh","-c","pwd"],"cwd":"subdir"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "subdir") {
		t.Errorf("expected cwd=subdir in output: %q", out)
	}
	// Path traversal must fail.
	_, err = reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["sh","-c","pwd"],"cwd":"../../etc"}`,
	))
	if err == nil {
		t.Fatal("expected error for path-traversal cwd")
	}
}

func TestRunCommandTimeoutSecondsRespected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep path")
	}

	t.Run("per_call_timeout_below_cap_applies", func(t *testing.T) {
		// Tool cap 30s, per-call timeout_seconds=2. The effective timeout is the
		// min (2s): 'sleep 10' must be killed, not allowed to run to exit=0.
		_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 30})
		start := time.Now()
		out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
			`{"argv":["sh","-c","sleep 10"],"timeout_seconds":2}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "exit=timeout") {
			t.Fatalf("expected exit=timeout (per-call 2s honored), got %q", out)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("per-call timeout not applied, took %s", elapsed)
		}
	})

	t.Run("tool_cap_applies_when_timeout_unset", func(t *testing.T) {
		// Tool cap 1s, timeout_seconds unset. The tool cap must still bound the
		// call: 'sleep 10' is killed at ~1s.
		_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 1})
		start := time.Now()
		out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
			`{"argv":["sh","-c","sleep 10"]}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "exit=timeout") {
			t.Fatalf("expected exit=timeout (tool cap 1s applies), got %q", out)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("tool cap not applied, took %s", elapsed)
		}
	})

	t.Run("fast_command_succeeds_without_timeout", func(t *testing.T) {
		// Tool cap 30s, timeout_seconds unset. A fast command completes with
		// exit=0 before any deadline fires.
		_, reg := setupWSWithOpts(t, DefaultOptions{RunAllowlist: []string{"sh"}, RunTimeoutSec: 30})
		out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
			`{"argv":["sh","-c","echo fast"]}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "fast") {
			t.Errorf("expected output: %q", out)
		}
		if !strings.Contains(out, "exit=0") {
			t.Errorf("expected exit=0: %q", out)
		}
	})
}

func TestGrepNestedAndGlob(t *testing.T) {
	ws, reg := setupWS(t)
	// Nested tree with matches and non-matches.
	paths := map[string]string{
		"root.go":             "package root\nconst Root = 1\n",
		"pkg/a.go":            "package pkg\nfunc Alpha() {}\n",
		"pkg/nested/b.go":     "package nested\nfunc Beta() {}\n",
		"pkg/nested/c.txt":    "no code here Beta word\n",
		"pkg/nested/skip.bin": "ignore",
	}
	for p, body := range paths {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	out, err := reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"func Beta","glob":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/nested/b.go") {
		t.Fatalf("grep nested: %q", out)
	}
	// glob *.go should not require full path pattern
	out, err = reg.Execute(ctx, "glob", json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root.go", "a.go", "b.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("glob missing %s: %q", want, out)
		}
	}
	// grep skips .env-like
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET_KEY=findme\n"), 0o600)
	out, err = reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"SECRET_KEY"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "SECRET_KEY") && strings.Contains(out, ".env") {
		t.Fatalf("grep should skip .env: %q", out)
	}
}

func TestGrepMaxMatchesTruncation(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// maxMatches defaults to 0 (uncapped); test with explicit 50.
	reg := NewRegistry()
	reg.Register(&grepTool{ws: ws, maxMatches: 50})
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "match-line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "many.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"match-line"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		// 50 max - 100 lines should truncate
		lines := strings.Count(out, "match-line")
		if lines > 50 {
			t.Fatalf("expected truncation, lines=%d out=%q", lines, out)
		}
	}
}

// TestGrepGlobPathForms covers the glob forms a caller actually writes.
//
// The filter matched only the base name, so every path-shaped glob - most
// importantly "**/*.md", the very form the sibling glob tool's description
// recommends - matched nothing and grep looked broken for markdown.
func TestGrepGlobPathForms(t *testing.T) {
	ws, reg := setupWS(t)
	files := map[string]string{
		"README.md":           "# Root\nneedle here\n",
		"docs/guide.md":       "# Guide\nneedle here\n",
		"docs/deep/notes.md":  "# Notes\nneedle here\n",
		"docs/deep/notes.txt": "needle here\n",
		"src/main.go":         "// needle here\n",
	}
	for p, body := range files {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	cases := []struct {
		glob string
		want []string
		deny []string
	}{
		{glob: "*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"notes.txt", "main.go"}},
		{glob: "**/*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"notes.txt", "main.go"}},
		{glob: "docs/**/*.md", want: []string{"docs/guide.md", "docs/deep/notes.md"}, deny: []string{"README.md", "main.go"}},
		{glob: "*.MD", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"main.go"}},
		{glob: "src/*.go", want: []string{"src/main.go"}, deny: []string{"README.md"}},
	}
	for _, tc := range cases {
		payload := fmt.Sprintf(`{"pattern":"needle","glob":%q}`, tc.glob)
		out, err := reg.Execute(ctx, "grep", json.RawMessage(payload))
		if err != nil {
			t.Fatalf("glob %q: %v", tc.glob, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Fatalf("glob %q missing %s in:\n%s", tc.glob, w, out)
			}
		}
		for _, d := range tc.deny {
			if strings.Contains(out, d) {
				t.Fatalf("glob %q wrongly matched %s in:\n%s", tc.glob, d, out)
			}
		}
	}
}

// TestGlobToolPathForms pins that the glob tool agrees with grep's filter.
// Two matchers meant "**/*.md" behaved differently in the two tools, and
// "docs/**/*.md" missed anything deeper than one level.
func TestGlobToolPathForms(t *testing.T) {
	ws, reg := setupWS(t)
	for _, p := range []string{"README.md", "docs/guide.md", "docs/deep/notes.md", "src/main.go"} {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cases := []struct {
		pattern string
		want    []string
		deny    []string
	}{
		{pattern: "**/*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"main.go"}},
		{pattern: "docs/**/*.md", want: []string{"docs/guide.md", "docs/deep/notes.md"}, deny: []string{"README.md"}},
	}
	for _, tc := range cases {
		out, err := reg.Execute(ctx, "glob", json.RawMessage(fmt.Sprintf(`{"pattern":%q}`, tc.pattern)))
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Fatalf("pattern %q missing %s in:\n%s", tc.pattern, w, out)
			}
		}
		for _, d := range tc.deny {
			if strings.Contains(out, d) {
				t.Fatalf("pattern %q wrongly matched %s in:\n%s", tc.pattern, d, out)
			}
		}
	}
}
