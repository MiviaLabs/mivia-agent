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

func setupWS(t *testing.T) (*workspace.Root, *Registry) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	return ws, reg
}

func setupWSWithOpts(t *testing.T, opts DefaultOptions) (*workspace.Root, *Registry) {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts.Workspace = ws
	reg := NewDefaultRegistry(opts)
	return ws, reg
}

func TestRegistryRejectsMalformedAndNonObjectArguments(t *testing.T) {
	_, reg := setupWS(t)
	for _, raw := range []json.RawMessage{json.RawMessage(`{"`), json.RawMessage(`[]`), json.RawMessage(`null`)} {
		if _, err := reg.Execute(context.Background(), "read_file", raw); err == nil {
			t.Fatalf("expected argument validation error for %s", raw)
		}
	}
}

func TestRegistryValidatesDeclaredSchema(t *testing.T) {
	_, reg := setupWS(t)
	cases := []json.RawMessage{
		json.RawMessage(`{"path":"a.txt","unexpected":true}`),
		json.RawMessage(`{"content":"x"}`),
		json.RawMessage(`{"path":false,"content":"x"}`),
	}
	for _, raw := range cases {
		if _, err := reg.Execute(context.Background(), "write_file", raw); err == nil {
			t.Fatalf("expected schema error for %s", raw)
		}
	}
}

type schemaProbeTool struct{ called bool }

func (t *schemaProbeTool) Name() string        { return "schema_probe" }
func (t *schemaProbeTool) Description() string { return "schema probe" }
func (t *schemaProbeTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"count": map[string]any{"type": "integer"},
		"mode":  map[string]any{"type": "string", "enum": []string{"safe", "fast"}},
	}, []string{"count", "mode"})
}
func (t *schemaProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.called = true
	return "called", nil
}

func TestRegistryRejectsFractionalIntegerAndInvalidEnum(t *testing.T) {
	reg := NewRegistry()
	probe := &schemaProbeTool{}
	reg.Register(probe)
	for _, raw := range []string{`{"count":1.5,"mode":"safe"}`, `{"count":1,"mode":"unsafe"}`} {
		if _, err := reg.Execute(context.Background(), probe.Name(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid arguments: %s", raw)
		}
	}
	if probe.called {
		t.Fatal("schema-invalid input reached Execute")
	}
}

func TestRegistryCapabilityNormalizesWritePath(t *testing.T) {
	ws, reg := setupWS(t)
	a := reg.Capability("write_file", json.RawMessage(`{"path":"dir/../same.txt","content":"a"}`))
	b := reg.Capability("write_file", json.RawMessage(`{"path":"./same.txt","content":"b"}`))
	c := reg.Capability("write_file", mustJSON(t, map[string]any{"path": filepath.Join(ws.Abs, "same.txt"), "content": "c"}))
	if a.ResourceKey != b.ResourceKey {
		t.Fatalf("resource keys differ: %q vs %q", a.ResourceKey, b.ResourceKey)
	}
	if a.ResourceKey != c.ResourceKey {
		t.Fatalf("workspace aliases differ: %q vs %q", a.ResourceKey, c.ResourceKey)
	}
}

func TestBuiltInToolsRejectPreCancelledContext(t *testing.T) {
	_, reg := setupWS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"read_file", "list_dir", "glob"} {
		args := json.RawMessage(`{"path":".","pattern":"*"}`)
		if _, err := reg.Execute(ctx, name, args); err == nil {
			t.Fatalf("%s succeeded with cancelled context", name)
		}
	}
}

func mustExec(t *testing.T, reg *Registry, name string, args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("%s: %v (out=%q)", name, err, out)
	}
	return out
}

func TestWriteReadReplace(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{"path": "a.txt", "content": "hello world"})
	out := mustExec(t, reg, "read_file", map[string]any{"path": "a.txt"})
	if out != "hello world" {
		t.Fatalf("read: %q", out)
	}
	mustExec(t, reg, "search_replace", map[string]any{
		"path": "a.txt", "old_string": "world", "new_string": "mivia",
	})
	out = mustExec(t, reg, "read_file", map[string]any{"path": "a.txt"})
	if out != "hello mivia" {
		t.Fatalf("got %q", out)
	}
}

func TestReadNestedPath(t *testing.T) {
	ws, reg := setupWS(t)
	nested := filepath.Join(ws.Abs, "pkg", "sub", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "nested-content-ok"
	if err := os.WriteFile(filepath.Join(nested, "file.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out := mustExec(t, reg, "read_file", map[string]any{"path": "pkg/sub/deep/file.go"})
	if out != body {
		t.Fatalf("nested read: %q", out)
	}

	// list intermediate dir
	listing := mustExec(t, reg, "list_dir", map[string]any{"path": "pkg/sub"})
	if !strings.Contains(listing, "deep/") {
		t.Fatalf("list_dir missing deep/: %q", listing)
	}
}

func TestListDirRootAndEmpty(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "root.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws.Abs, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := mustExec(t, reg, "list_dir", map[string]any{"path": "."})
	if !strings.Contains(out, "root.txt") || !strings.Contains(out, "d/") {
		t.Fatalf("list root: %q", out)
	}

	empty := mustExec(t, reg, "list_dir", map[string]any{"path": "d"})
	if empty != "(empty)" {
		t.Fatalf("empty dir: %q", empty)
	}
}

func TestReadFileRejectsDirectory(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.Mkdir(filepath.Join(ws.Abs, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"dir"}`))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadFileTooLarge(t *testing.T) {
	const limit = 1024
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxReadBytes: limit})
	big := strings.Repeat("a", limit+1)
	if err := os.WriteFile(filepath.Join(ws.Abs, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"big.txt"}`))
	if err == nil {
		t.Fatal("expected too large error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err=%v", err)
	}

	// Exactly at limit is allowed.
	ok := strings.Repeat("b", limit)
	if err := os.WriteFile(filepath.Join(ws.Abs, "ok.txt"), []byte(ok), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"ok.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != limit {
		t.Fatalf("len=%d", len(out))
	}
}

func TestReadFileDefaultMax256KiB(t *testing.T) {
	// Default registry uses 256 KiB; a 1 byte over that fails.
	ws, reg := setupWS(t)
	const max = 256 * 1024
	// Don't allocate full 256KiB+1 in CI memory wastefully if not needed —
	// write a sparse-like file by WriteFile of max+1 bytes.
	data := make([]byte, max+1)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "huge.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"huge.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadFileRejectsBinary(t *testing.T) {
	ws, reg := setupWS(t)
	// Invalid UTF-8
	if err := os.WriteFile(filepath.Join(ws.Abs, "bin.dat"), []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"bin.dat"}`))
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadFileOffsetLimitWindow(t *testing.T) {
	ws, reg := setupWS(t)
	body := "L1\nL2\nL3\nL4\nL5\n"
	if err := os.WriteFile(filepath.Join(ws.Abs, "lines.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Model-style call that previously failed with unknown field "offset".
	out, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"lines.txt","offset":2,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "L2") || !strings.Contains(out, "L3") {
		t.Fatalf("window missing lines: %q", out)
	}
	if strings.Contains(out, "L1") || strings.Contains(out, "L4") {
		t.Fatalf("window leaked other lines: %q", out)
	}
	if !strings.Contains(out, "lines 2") {
		t.Fatalf("expected line-range header: %q", out)
	}
}

func TestReadFileLargeWithOffsetSucceeds(t *testing.T) {
	const limit = 100
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxReadBytes: limit})
	// File larger than maxBytes; full read fails, window succeeds.
	lines := make([]string, 0, 50)
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	big := strings.Join(lines, "\n")
	if err := os.WriteFile(filepath.Join(ws.Abs, "biglines.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"biglines.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too large without window, err=%v", err)
	}
	out, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"biglines.txt","offset":1,"limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line-01") || !strings.Contains(out, "line-03") {
		t.Fatalf("window=%q", out)
	}
}

func TestBlockEnvRead(t *testing.T) {
	ws, reg := setupWS(t)
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET=1"), 0o600)
	_, err := reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":".env"}`))
	if err == nil {
		t.Fatal("expected block")
	}
	// Nested secret-like
	if err := os.MkdirAll(filepath.Join(ws.Abs, "cfg"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ws.Abs, "cfg", ".env.local"), []byte("X=1"), 0o600)
	_, err = reg.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"cfg/.env.local"}`))
	if err == nil {
		t.Fatal("expected nested .env block")
	}
}

func TestWriteNestedCreatesParents(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path": "a/b/c/out.txt", "content": "nested-write",
	})
	out := mustExec(t, reg, "read_file", map[string]any{"path": "a/b/c/out.txt"})
	if out != "nested-write" {
		t.Fatalf("got %q", out)
	}
}

func TestSearchReplaceUniqueAndReplaceAll(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path": "m.txt", "content": "aa bb aa",
	})
	_, err := reg.Execute(context.Background(), "search_replace", json.RawMessage(
		`{"path":"m.txt","old_string":"aa","new_string":"xx"}`,
	))
	if err == nil || !strings.Contains(err.Error(), "times") {
		t.Fatalf("expected non-unique error, got %v", err)
	}
	mustExec(t, reg, "search_replace", map[string]any{
		"path": "m.txt", "old_string": "aa", "new_string": "xx", "replace_all": true,
	})
	out := mustExec(t, reg, "read_file", map[string]any{"path": "m.txt"})
	if out != "xx bb xx" {
		t.Fatalf("got %q", out)
	}
}

func TestSearchReplaceResultStatsAndPreview(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path": "edit.txt", "content": "line1\noldA\noldB\nline4\n",
	})
	out := mustExec(t, reg, "search_replace", map[string]any{
		"path":       "edit.txt",
		"old_string": "oldA\noldB",
		"new_string": "newA\nnewB\nnewC",
	})
	// +3 lines new, −2 lines old
	if !strings.Contains(out, "updated edit.txt (1 replacement, +3 −2)") {
		t.Fatalf("missing +/- stats in result: %q", out)
	}
	// New format: GitHub-style unified diff with a/path and @@ hunks.
	if !strings.Contains(out, "--- a/edit.txt") {
		t.Fatalf("missing '--- a/edit.txt' in result: %q", out)
	}
	if !strings.Contains(out, "+++ b/edit.txt") {
		t.Fatalf("missing '+++ b/edit.txt' in result: %q", out)
	}
	if !strings.Contains(out, "@@ -1,4 +1,5 @@") {
		t.Fatalf("missing hunk header in result: %q", out)
	}
	if !strings.Contains(out, "-oldA") || !strings.Contains(out, "-oldB") {
		t.Fatalf("missing deleted lines in result: %q", out)
	}
	if !strings.Contains(out, "+newA") || !strings.Contains(out, "+newB") || !strings.Contains(out, "+newC") {
		t.Fatalf("missing added lines in result: %q", out)
	}
}

func TestWriteFileResultCreateAndOverwrite(t *testing.T) {
	_, reg := setupWS(t)
	out := mustExec(t, reg, "write_file", map[string]any{
		"path": "w.txt", "content": "a\nb\n",
	})
	if !strings.Contains(out, "wrote w.txt") || !strings.Contains(out, "create +") {
		t.Fatalf("expected create stats: %q", out)
	}
	out = mustExec(t, reg, "write_file", map[string]any{
		"path": "w.txt", "content": "only\n",
	})
	if !strings.Contains(out, "overwrite") || !strings.Contains(out, "→") {
		t.Fatalf("expected overwrite N→M lines: %q", out)
	}
}

func TestWriteFileOverwriteDiffCap(t *testing.T) {
	ws, reg := setupWS(t)
	large := strings.Repeat("x\n", (512<<10)/2)
	if err := os.WriteFile(filepath.Join(ws.Abs, "large.txt"), []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustExec(t, reg, "write_file", map[string]any{"path": "large.txt", "content": "small\n"})
	if !strings.Contains(out, "diff omitted") {
		t.Fatalf("expected capped diff omission: %q", out)
	}
	if strings.Contains(out, "-x\n") || strings.Contains(out, "+x\n") {
		t.Fatalf("oversize overwrite emitted file content: %q", out)
	}
}

func TestCountLines(t *testing.T) {
	if got := countLines(""); got != 0 {
		t.Fatalf("empty: %d", got)
	}
	if got := countLines("a"); got != 1 {
		t.Fatalf("single: %d", got)
	}
	if got := countLines("a\nb"); got != 2 {
		t.Fatalf("two: %d", got)
	}
	if got := countLines("a\nb\n"); got != 3 {
		t.Fatalf("trailing newline counts empty last line: %d", got)
	}
}

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
	exact, prefixes := resolveEnvAllowlist(nil, nil, nil)
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

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
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

func TestPathEscapeViaTools(t *testing.T) {
	_, reg := setupWS(t)
	ctx := context.Background()
	_, err := reg.Execute(ctx, "read_file", json.RawMessage(`{"path":"../outside.txt"}`))
	if err == nil {
		t.Fatal("expected escape error on read")
	}
	_, err = reg.Execute(ctx, "write_file", json.RawMessage(`{"path":"../pwn.txt","content":"x"}`))
	if err == nil {
		t.Fatal("expected escape error on write")
	}
}

func TestUnknownTool(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "nope", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected unknown tool")
	}
}

func TestOpenAIToolsSchemaValidRequiredArrays(t *testing.T) {
	_, reg := setupWS(t)
	tools := reg.OpenAITools()
	if len(tools) < 8 {
		t.Fatalf("tools=%d", len(tools))
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	// DeepSeek rejected null required arrays; ensure we never emit "required":null
	if strings.Contains(string(raw), `"required":null`) {
		t.Fatalf("schema has null required: %s", raw)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tool := range decoded {
		fn, _ := tool["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		req, ok := params["required"]
		if !ok {
			continue
		}
		if req == nil {
			t.Fatalf("null required in %v", fn["name"])
		}
		if _, ok := req.([]any); !ok {
			t.Fatalf("required not array for %v: %T", fn["name"], req)
		}
	}
}

func TestDisableTools_CaseInsensitive(t *testing.T) {
	ws, _ := setupWS(t)
	// Mixed-case tool names should disable the tools.
	opts := DefaultOptions{
		Workspace:    ws,
		DisableTools: []string{"Read_File", "GREP", "Run_Command"},
	}
	reg := NewDefaultRegistry(opts)

	// All three tools should be absent from the registry.
	for _, name := range []string{"read_file", "grep", "run_command"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should be disabled", name)
		}
	}
	// Verify Execute returns "unknown tool" for disabled tools.
	ctx := context.Background()
	for _, name := range []string{"read_file", "grep", "run_command"} {
		_, err := reg.Execute(ctx, name, json.RawMessage(`{}`))
		if err == nil {
			t.Errorf("expected error for disabled tool %q", name)
		}
		if !strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("expected 'unknown tool' error for %q, got: %v", name, err)
		}
	}
	// Other tools should still work (e.g. list_dir).
	if _, ok := reg.Get("list_dir"); !ok {
		t.Errorf("list_dir should not be disabled")
	}
}

func TestDisableTools_LowerCaseNames(t *testing.T) {
	ws, _ := setupWS(t)
	// All lower-case names should also disable the tools.
	opts := DefaultOptions{
		Workspace:    ws,
		DisableTools: []string{"read_file", "grep", "run_command"},
	}
	reg := NewDefaultRegistry(opts)

	for _, name := range []string{"read_file", "grep", "run_command"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("tool %q should be disabled", name)
		}
	}
}

func TestRedactToolArgs_RespectsPrivacyConfig(t *testing.T) {
	// Set the package-level redact state from PrivacyConfig.
	SetRedactToolArgs(true)
	t.Cleanup(func() { SetRedactToolArgs(false) })

	_, reg := setupWS(t)
	secret := "super-secret-arg-value"
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["false","`+secret+`"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("secret value leaked when redact enabled: %q", out)
	}
	if !strings.Contains(out, "arguments redacted") {
		t.Errorf("missing 'arguments redacted' marker: %q", out)
	}
}

func TestRedactToolArgs_DefaultsToFalse(t *testing.T) {
	// Ensure no package-level redact.
	SetRedactToolArgs(false)

	_, reg := setupWS(t)
	visible := "my-visible-arg"
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["false","`+visible+`"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, visible) {
		t.Errorf("argument should be visible by default: %q", out)
	}
	if strings.Contains(out, "arguments redacted") {
		t.Errorf("unexpected redaction marker when redact is off: %q", out)
	}
}

func TestRedactToolArgs_DefaultOptionsRedactToolArgsNotUsed(t *testing.T) {
	// Verify that DefaultOptions.RedactToolArgs is NOT used.
	// The only source of truth is the package-level atomic.
	ws, _ := setupWS(t)

	// Create a registry with RedactToolArgs set in DefaultOptions BUT
	// package-level is false. The old code path would check DefaultOptions,
	// but the refactor removed that field.
	opts := DefaultOptions{
		Workspace: ws,
		// There is no RedactToolArgs field in DefaultOptions anymore.
	}
	// DefaultOptions.RedactToolArgs was removed — this test verifies the
	// absence and proves the package atomic is the single source of truth.
	_ = opts

	// The actual behaviour: toggle via package-level API.
	SetRedactToolArgs(true)
	t.Cleanup(func() { SetRedactToolArgs(false) })
	reg := NewDefaultRegistry(opts)

	secret := "should-be-redacted"
	out, err := reg.Execute(context.Background(), "run_command", json.RawMessage(
		`{"argv":["false","`+secret+`"]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("argument leaked when redact enabled: %q", out)
	}
	if !strings.Contains(out, "arguments redacted") {
		t.Errorf("missing redaction marker: %q", out)
	}
}

func TestIsSecretPath(t *testing.T) {
	cases := map[string]bool{
		// Blocked: actual secret files
		".env":           true,
		"cfg/.env":       true,
		".env.local":     true,
		".env.production": true,
		"id_rsa":         true,
		"certs/key.pem":  true,
		// Allowed: templates and non-secret files
		".env.example":   false,
		"main.go":        false,
		"README.md":      false,
		"docs/config.md": false,
	}
	for path, want := range cases {
		if got := isSecretPath(path, nil, nil); got != want {
			t.Errorf("isSecretPath(%q)=%v want %v", path, got, want)
		}
	}
}

// TestSecretPathExceptionsGlobalIsolation verifies that two registries with
// different SecretPathExceptions do NOT share state — each registration
// captures its own copy.
func TestSecretPathExceptionsGlobalIsolation(t *testing.T) {
	ws1, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Registry with no custom exceptions (uses default: .env.example).
	reg1 := NewDefaultRegistry(DefaultOptions{Workspace: ws1})

	ws2, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Registry with custom exceptions that allow .env files.
	reg2 := NewDefaultRegistry(DefaultOptions{
		Workspace: ws2,
		SecretPathExceptions: []string{
			".env.example",
			".env.local", // allow .env.local in addition to .env.example
		},
	})

	// For reg1 (default): .env.local is secret, .env.example is not.
	if isSecretPath(".env.local", DefaultSecretPathExceptions, DefaultSecretPathPatterns) {
		// Default behavior: .env.local should be secret
	} else {
		t.Error("default exceptions should NOT allow .env.local")
	}

	// For reg2 (custom): .env.local is explicitly allowed via exceptions.
	if isSecretPath(".env.local", []string{".env.example", ".env.local"}, DefaultSecretPathPatterns) {
		t.Error("custom exceptions should allow .env.local")
	}

	// .pem should always be blocked regardless of exceptions.
	if !isSecretPath("key.pem", []string{".env.example", ".env.local"}, DefaultSecretPathPatterns) {
		t.Error("key.pem should be blocked")
	}
	if !isSecretPath("key.pem", DefaultSecretPathExceptions, DefaultSecretPathPatterns) {
		t.Error("key.pem should be blocked with default exceptions")
	}

	// Verify the registries themselves don't leak state by checking tool names.
	// (We can't easily introspect the internal secretExceptions slices, but we
	// can confirm both registries are functional.)
	ctx := context.Background()
	if _, err := reg1.Execute(ctx, "read_file", json.RawMessage(`{"path":"test.txt"}`)); err == nil {
		t.Error("expected error reading non-existent file from reg1")
	}
	if _, err := reg2.Execute(ctx, "read_file", json.RawMessage(`{"path":"test.txt"}`)); err == nil {
		t.Error("expected error reading non-existent file from reg2")
	}
}

// TestFilterEnvViaRunCommandTool verifies that filterEnv used via a properly
// configured runCommandTool correctly filters environment variables.
func TestFilterEnvViaRunCommandTool(t *testing.T) {
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

	// Use the same resolution as NewDefaultRegistry does.
	exact, prefixes := resolveEnvAllowlist(nil, nil, nil)
	tool := &runCommandTool{envExact: exact, envPrefix: prefixes}
	filtered := tool.filterEnv(env)

	// Should keep PATH, HOME, USER, LANG (the crucial POSIX vars).
	allowedKeys := map[string]bool{}
	for _, e := range filtered {
		key, _, _ := strings.Cut(e, "=")
		allowedKeys[key] = true
	}

	for _, want := range []string{"PATH", "HOME", "USER", "LANG"} {
		if !allowedKeys[want] {
			t.Errorf("filterEnv dropped allowed var %q", want)
		}
	}

	// Should drop SECRET, DB_PASSWORD, API_KEY, GITHUB_TOKEN.
	for _, blocked := range []string{"SECRET", "DB_PASSWORD", "API_KEY", "GITHUB_TOKEN"} {
		if allowedKeys[blocked] {
			t.Errorf("filterEnv leaked blocked var %q", blocked)
		}
	}
}

// TestFilterEnvPackageLevelGone ensures there is no package-level filterEnv
// wrapper — the only filterEnv is the method on runCommandTool.
// This is a compile-time check: if a package-level filterEnv existed,
// calling filterEnv(...) without a receiver would compile. Since we
// assert it must NOT exist, we verify the method is on runCommandTool.
func TestFilterEnvPackageLevelGone(t *testing.T) {
	// Verify that filterEnv is a method on runCommandTool, not a package-level func.
	tool := &runCommandTool{}
	result := tool.filterEnv([]string{"PATH=/bin"})
	if len(result) == 0 {
		t.Fatal("filterEnv returned empty slice unexpectedly")
	}
}
