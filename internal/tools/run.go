package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type runCommandTool struct {
	ws         *workspace.Root
	allowlist  []string
	timeoutSec int
	maxOut     int
	// memoryBackstop is the OOM-guard byte budget declared to the dispatcher
	// when maxOut is uncapped (0). Defaults to defaultMemoryBackstopBytes.
	memoryBackstop int
	// redactArgs when true hides argv in the model-visible header.
	// Defaults from package RedactToolArgs() / DefaultOptions.
	redactArgs bool
	// envAllow and envBlock override the deprecated isAllowedEnvVar.
	// When non-nil, filterEnv uses these sets instead.
	envExact             map[string]bool
	envPrefix            []string
	envKeywordBlock      []string
	secretPathExceptions []string
	secretPathPatterns   []string
}

func (t *runCommandTool) Capability(json.RawMessage) Capability {
	// Advertise the process budget so the agent loop can grant more than the
	// default ToolTimeout (60s). Without this, long builds/tests die at 60s
	// even though run_command itself is configured for minutes.
	timeout := time.Duration(t.timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 900 * time.Second
	}
	// Capability.MaxResultBytes is deliberately NOT declared: the agent loop
	// treats it as a wire truncation bound, and the result header (argv echo,
	// cwd, exit status) rides above maxOut. The capture budget feeds the
	// dispatcher backstop via ResultBudgetBytes instead.
	return Capability{Class: ExecutionExternal, Timeout: timeout}
}

// ResultBudgetBytes declares the capture budget for dispatcher ceiling
// derivation. A positive maxOut is the operator bound. When maxOut is 0
// (uncapped defaults), declare the memory OOM backstop so honest multi-MB
// capture is not destroyed at the floor-derived ~256KiB ceiling.
func (t *runCommandTool) ResultBudgetBytes() int {
	if t.maxOut > 0 {
		return t.maxOut
	}
	if t.memoryBackstop > 0 {
		return t.memoryBackstop
	}
	return defaultMemoryBackstopBytes
}

// RunCommandToolName is the registry name of the shell-exec tool. It is the
// only tool that reports a child failure in its body while Execute returns
// err=nil, so status readers must recognise it by name.
const RunCommandToolName = "run_command"

func (t *runCommandTool) Name() string { return RunCommandToolName }
func (t *runCommandTool) Description() string {
	return "LAST RESORT: run an allowlisted program as argv (no shell string). " +
		"Params: argv (string array; argv[0] is bare program name on allowlist). Prefer over shell commands. " +
		"Prefer read_file (with offset/limit), list_dir, grep, glob, write_file, search_replace, multi_edit for file work. " +
		"Do not invent shell tools (bash, grep, wc). Examples: [\"make\",\"test\"], [\"git\",\"status\"], [\"npm\",\"test\"]."
}
func (t *runCommandTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"argv": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Argument vector; argv[0] is the bare program name (must be allowlisted). Not a shell string.",
		},
		"timeout_seconds": map[string]any{
			"type":        "integer",
			"description": "Optional per-call timeout in seconds, clamped to the tool-level maximum.",
		},
		"cwd": map[string]any{
			"type":        "string",
			"description": "Optional workspace-relative working directory for the command.",
		},
		"stdin": map[string]any{
			"type":        "string",
			"description": "Optional string to pipe to the command's standard input.",
		},
	}, []string{"argv"})
}

func (t *runCommandTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Argv           []string `json:"argv"`
		TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
		Cwd            string   `json:"cwd,omitempty"`
		Stdin          string   `json:"stdin,omitempty"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	bin, commandArgs, err := t.resolveCommand(in.Argv)
	if err != nil {
		return "", err
	}
	// Same policy as read_file/write: do not let allowlisted utilities (cat, head, …)
	// bypass secret-path blocks via argv. Fail closed before process start.
	if secret := secretPathInArgv(commandArgs, t.secretPathExceptions, t.secretPathPatterns); secret != "" {
		_ = secret // operand intentionally not surfaced: revealing which secret
		// path was blocked confirms its existence to the model. The message is
		// static and safe to surface verbatim via the dispatcher error path.
		return "", fmt.Errorf("accessing secret-like path is blocked")
	}

	callCtx, cancel := t.callContext(ctx, in.TimeoutSeconds)
	if cancel != nil {
		defer cancel()
	}
	cmdDir, err := t.resolveCwd(in.Cwd)
	if err != nil {
		return "", err
	}
	cmd, scope, err := t.buildCommand(callCtx, bin, commandArgs, cmdDir, in.Stdin)
	if err != nil {
		return "", err
	}
	defer scope.cleanup()

	capture := t.runCapture(cmd, callCtx, scope)
	return t.composeResult(in.Argv, callCtx, capture.runErr, cmdDir, capture), nil
}

// callContext derives the effective per-call timeout context, clamped to the
// tool-level maximum and only applied when tighter than the parent's deadline
// (never extending it). The cancel func is nil when the parent deadline
// already bounds the call.
func (t *runCommandTool) callContext(ctx context.Context, requested int) (context.Context, context.CancelFunc) {
	effectiveTimeout := t.timeoutSec
	if requested > 0 && requested < effectiveTimeout {
		effectiveTimeout = requested
	}
	timeout := time.Duration(effectiveTimeout) * time.Second
	if parentDeadline, ok := ctx.Deadline(); !ok || timeout < time.Until(parentDeadline) {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, nil
}

// resolveCwd validates a per-call workspace-relative working directory,
// defaulting to the workspace root.
func (t *runCommandTool) resolveCwd(cwd string) (string, error) {
	if cwd == "" {
		return t.ws.Abs, nil
	}
	absCwd, err := t.ws.Resolve(cwd)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	return absCwd, nil
}

// buildCommand assembles the *exec.Cmd with its process scope, working
// directory, filtered minimal env and optional stdin.
func (t *runCommandTool) buildCommand(callCtx context.Context, bin string, commandArgs []string, cmdDir, stdin string) (*exec.Cmd, commandScope, error) {
	cmd := exec.CommandContext(callCtx, bin, commandArgs...)
	cmd.WaitDelay = 2 * time.Second
	scope, err := prepareCommand(cmd)
	if err != nil {
		return nil, commandScope{}, fmt.Errorf("prepare command process scope: %w", err)
	}
	cmd.Cancel = func() error { return scope.cancel(cmd) }
	cmd.Dir = cmdDir
	// Minimal env: keep PATH and essential vars; strip obvious secrets is hard - do not pass extra.
	cmd.Env = t.filterEnv(os.Environ())
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd, scope, nil
}

// runCapture is the dual-stream output of a completed run_command invocation.
type runCapture struct {
	stdout, stderr string
	truncated      bool
	runErr         error
}

// runCapture executes the prepared command against one shared maxOut budget
// across stdout+stderr (not 2×maxOut peak); writes still succeed fully
// (process is not stalled on a full pipe). It returns the captured output
// streams and the wait error (nil for a clean exit; child failures surface in
// the result body instead).
func (t *runCommandTool) runCapture(cmd *exec.Cmd, callCtx context.Context, scope commandScope) runCapture {
	cap := newDualCapture(t.maxOut)
	cmd.Stdout = cap.Stdout()
	cmd.Stderr = cap.Stderr()
	var runErr error
	if err := cmd.Start(); err != nil {
		runErr = err
	} else if err := scope.attach(cmd); err != nil {
		_ = scope.cancel(cmd)
		_ = waitCommand(cmd, callCtx, scope)
		runErr = err
	} else {
		runErr = waitCommand(cmd, callCtx, scope)
	}
	return runCapture{
		stdout:    cap.StdoutString(),
		stderr:    cap.StderrString(),
		truncated: t.maxOut > 0 && cap.Truncated(),
		runErr:    runErr,
	}
}

// composeResult renders the model-visible result: redacted stdout/stderr with
// their headers, the truncation notice, and the exit-status header. The body
// is returned as the tool result, so the policy decides what the model sees,
// not just what the operator sees.
func (t *runCommandTool) composeResult(argv []string, callCtx context.Context, runErr error, cmdDir string, capture runCapture) string {
	stdoutText := redact.Text(capture.stdout)
	stderrText := redact.Text(capture.stderr)

	header := t.formatResultHeader(argv, callCtx, runErr, cmdDir)
	var body strings.Builder
	hasOutput := false
	if stdoutText != "" {
		body.WriteString("stdout:\n")
		body.WriteString(stdoutText)
		hasOutput = true
	}
	if stderrText != "" {
		if hasOutput {
			body.WriteString("\n")
		}
		body.WriteString("stderr:\n")
		body.WriteString(stderrText)
		hasOutput = true
	}
	if capture.truncated {
		body.WriteString(fmt.Sprintf("\n... truncated at %d bytes", t.maxOut))
	}
	if !hasOutput && !capture.truncated {
		return header + "(no output)"
	}
	return header + body.String()
}

// waitCommand waits for cmd, but after ctx is done kills the tree and abandons
// Wait if the process is unreapable (e.g. D-state) so the tool slot frees.
// The cmd.Wait goroutine may briefly leak if the process enters an unkillable
// state; it will be reclaimed when the kernel eventually reaps the child.
func waitCommand(cmd *exec.Cmd, ctx context.Context, scope commandScope) error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = scope.cancel(cmd)
		// WaitDelay already set by CommandContext; give a short extra reap grace.
		grace := cmd.WaitDelay
		if grace <= 0 {
			grace = 2 * time.Second
		}
		grace += 3 * time.Second
		select {
		case err := <-done:
			return err
		case <-time.After(grace):
			return ctx.Err()
		}
	}
}

func (t *runCommandTool) formatResultHeader(argv []string, callCtx context.Context, runErr error, cwd string) string {
	status := exitStatus(callCtx, runErr)
	argPart := FormatArgv(argv)
	if t.redactArgs || RedactToolArgs() {
		argPart = argv[0] + " [arguments redacted]"
	} else {
		argPart = redact.Text(argPart)
	}
	return fmt.Sprintf("command: %s\ncwd: %s\n%s\n", argPart, cwd, status)
}

func exitStatus(callCtx context.Context, runErr error) string {
	if runErr == nil {
		return "exit=0"
	}
	switch {
	case callCtx.Err() == context.DeadlineExceeded:
		return "exit=timeout"
	case callCtx.Err() == context.Canceled:
		return "exit=canceled"
	default:
		if ee, ok := runErr.(*exec.ExitError); ok {
			return fmt.Sprintf("exit=%d", ee.ExitCode())
		}
		return "exit=error"
	}
}

func (t *runCommandTool) resolveCommand(argv []string) (string, []string, error) {
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("argv must be non-empty")
	}
	bin := argv[0]
	if strings.Contains(bin, string(os.PathSeparator)) || strings.Contains(bin, "/") || strings.Contains(bin, "\\") {
		return "", nil, fmt.Errorf("program must be a bare name on the allowlist, not a path: %q", bin)
	}
	if !t.allowed(bin) {
		return "", nil, fmt.Errorf("program %q is not allowlisted (allowed: %s)", bin, strings.Join(t.allowlist, ", "))
	}
	if runtime.GOOS == "windows" && (bin == "echo" || bin == "true" || bin == "false") {
		return "", nil, fmt.Errorf("program %q is not available without a shell on Windows", bin)
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", nil, fmt.Errorf("program not found on PATH: %s", bin)
	}
	return resolved, argv[1:], nil
}

func (t *runCommandTool) allowed(bin string) bool {
	base := filepath.Base(bin)
	binLower := strings.ToLower(bin)
	baseLower := strings.ToLower(base)
	for _, a := range t.allowlist {
		if a == binLower || a == baseLower {
			return true
		}
	}
	return false
}

func (t *runCommandTool) filterEnv(env []string) []string {
	// A nil exactSet is an empty allowlist, not a request for defaults: with
	// nothing configured, no variable is passed through.
	exactSet := t.envExact
	prefixSet := t.envPrefix

	var out []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		uk := strings.ToUpper(key)
		if !exactSet[uk] {
			matched := false
			for _, p := range prefixSet {
				if strings.HasPrefix(uk, p) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if t.containsBlockedKeyword(uk) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// containsBlockedKeyword screens variables admitted by a prefix rule. It is
// subtractive only: an exact env_allowlist entry is never dropped, so a build
// that genuinely needs FOO_TOKEN names it outright.
func (t *runCommandTool) containsBlockedKeyword(s string) bool {
	for _, kw := range t.envKeywordBlock {
		if kw != "" && strings.Contains(s, strings.ToUpper(kw)) {
			return true
		}
	}
	return false
}

// The environment allowlist is configuration-only. No variable names or
// prefixes are compiled in: which variables a child process may see is
// workspace policy. Recommended values ship in .mivia/mivia.toml.example under
// [tools].env_allowlist, where a trailing "*" declares a prefix rule
// ("GIT_*"). With it unset, child processes inherit no environment.
//
// [tools].env_allow_keyword_blocklist is the companion subtractive filter for
// prefix matches; it too has no compiled-in value.

// resolveEnvAllowlist computes the effective env var allowlist from the
// built-in defaults plus configurable overrides. Resolution order:
//
//	config.EnvAllowlist (or config.EnvAllowlistOnly)
//	  → config.EnvBlocklist (removed)
//
// Entries in cfgEnvAllow / cfgEnvAllowOnly ending in "*" are treated as
// prefix rules (e.g. "GIT_*" matches GIT_DIR, GIT_WORK_TREE, etc.).
func resolveEnvAllowlist(cfgEnvAllow, cfgEnvAllowOnly, cfgEnvBlock []string) (exactSet map[string]bool, prefixSet []string) {
	// With no compiled-in list there is nothing to extend or replace, so
	// env_allowlist_only and env_allowlist differ only in name; both are
	// honoured so existing configs keep working.
	var base []string
	if len(cfgEnvAllowOnly) > 0 {
		cfgEnvAllow = cfgEnvAllowOnly
	}

	// Separate wildcard (prefix) entries from exact entries.
	var extraPrefixes []string
	for _, v := range cfgEnvAllow {
		if strings.HasSuffix(v, "*") {
			p := strings.TrimSuffix(v, "*")
			extraPrefixes = append(extraPrefixes, p)
		} else {
			base = append(base, v)
		}
	}

	// Build blocklist set (uppercased).
	blocked := make(map[string]bool, len(cfgEnvBlock))
	for _, v := range cfgEnvBlock {
		blocked[strings.ToUpper(v)] = true
	}
	blockedPrefixes := make(map[string]bool)
	for _, v := range cfgEnvBlock {
		if strings.HasSuffix(v, "*") {
			blockedPrefixes[strings.ToUpper(strings.TrimSuffix(v, "*"))] = true
		}
	}

	// Apply blocklist and build exact set.
	exactSet = make(map[string]bool, len(base))
	for _, v := range base {
		uk := strings.ToUpper(v)
		if blocked[uk] {
			continue
		}
		exactSet[uk] = true
	}

	// Build prefix set from the configured wildcard entries, minus blocklist.
	allPrefixes := extraPrefixes
	prefixSet = make([]string, 0, len(allPrefixes))
	for _, p := range allPrefixes {
		up := strings.ToUpper(p)
		if blocked[up] || blockedPrefixes[up] {
			continue
		}
		prefixSet = append(prefixSet, up)
	}

	return exactSet, prefixSet
}
