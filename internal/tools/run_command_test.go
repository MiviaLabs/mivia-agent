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

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestRunAllowlist(t *testing.T) {
	_, reg := setupWS(t)
	ctx := context.Background()
	out, err := reg.Execute(ctx, "run_command", json.RawMessage(`{"argv":["echo","hi"]}`))
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
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["false",%q]}`, marker)))
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
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(fmt.Sprintf(`{"argv":["false",%q]}`, secret)))
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
	// false exits 1 but is allowlisted
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(`{"argv":["false"]}`))
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

func TestIsAllowedEnvVar(t *testing.T) {
	tests := []struct {
		key   string
		allow bool
	}{
		// Critical POSIX vars — must be allowed
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
		// Known secrets — must be blocked
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
		// Unknown vars — default blocked
		{"MY_CUSTOM_VAR", false},
		{"FOOBAR", false},
		{"PROJECT_HOME", false},
	}
	for _, tt := range tests {
		got := isAllowedEnvVar(tt.key)
		if got != tt.allow {
			t.Errorf("isAllowedEnvVar(%q) = %v, want %v", tt.key, got, tt.allow)
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
	exact, prefixes := resolveEnvAllowlist(testEnvAllowlist, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes}
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

func TestScrubSecrets_RedactsKeyPatterns(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"sk-abc123XYZ", "[redacted]"},
		{"ghp_abc123def456", "[redacted]"},
		{"github_pat_abc123", "[redacted]"},
		{"no-secret-here", "no-secret-here"},
		{"", ""},
	}
	for _, c := range cases {
		got := scrubSecrets(c.input)
		if got != c.expected {
			t.Errorf("scrubSecrets(%q) = %q, want %q", c.input, got, c.expected)
		}
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
	cancel() // already canceled parent — must not hang; status exit=canceled
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
	// NewDefaultRegistry uses maxMatches 50; create many matching lines.
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
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
		// 50 max — 100 lines should truncate
		lines := strings.Count(out, "match-line")
		if lines > 50 {
			t.Fatalf("expected truncation, lines=%d out=%q", lines, out)
		}
	}
}
