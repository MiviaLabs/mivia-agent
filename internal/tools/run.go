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

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type runCommandTool struct {
	ws         *workspace.Root
	allowlist  []string
	timeoutSec int
	maxOut     int
	// redactArgs when true hides argv in the model-visible header.
	// Defaults from package RedactToolArgs() / DefaultOptions.
	redactArgs bool
}

func (t *runCommandTool) Capability(json.RawMessage) Capability {
	// Advertise the process budget so the agent loop can grant more than the
	// default ToolTimeout (60s). Without this, long builds/tests die at 60s
	// even though run_command itself is configured for minutes.
	timeout := time.Duration(t.timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return Capability{Class: ExecutionExternal, Timeout: timeout}
}

func (t *runCommandTool) Name() string { return "run_command" }
func (t *runCommandTool) Description() string {
	return "LAST RESORT: run an allowlisted program as argv (no shell string). " +
		"Params: argv (string array; argv[0] is bare program name on allowlist). " +
		"Prefer read_file (with offset/limit), list_dir, grep, glob, write_file, search_replace for file work. " +
		"Do not invent shell tools (bash, grep, wc). Examples: [\"make\",\"test\"], [\"git\",\"status\"], [\"npm\",\"test\"]."
}
func (t *runCommandTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"argv": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Argument vector; argv[0] is the bare program name (must be allowlisted). Not a shell string.",
		},
	}, []string{"argv"})
}

func (t *runCommandTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Argv []string `json:"argv"`
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
	if secret := secretPathInArgv(commandArgs); secret != "" {
		return "", fmt.Errorf("accessing secret-like path is blocked: %s", secret)
	}
	resolved := bin

	timeout := time.Duration(t.timeoutSec) * time.Second
	// Only apply if tighter than parent's deadline — never extend.
	callCtx := ctx
	if parentDeadline, ok := ctx.Deadline(); !ok || timeout < time.Until(parentDeadline) {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(callCtx, resolved, commandArgs...)
	cmd.WaitDelay = 2 * time.Second
	scope, err := prepareCommand(cmd)
	if err != nil {
		return "", fmt.Errorf("prepare command process scope: %w", err)
	}
	defer scope.cleanup()
	cmd.Cancel = func() error { return scope.cancel(cmd) }
	cmd.Dir = t.ws.Abs
	// Minimal env: keep PATH and essential vars; strip obvious secrets is hard — do not pass extra.
	cmd.Env = filterEnv(os.Environ())

	// One shared maxOut budget across stdout+stderr (not 2×maxOut peak).
	// Writes still succeed fully (process is not stalled on a full pipe).
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

	out := cap.Combined()
	if t.maxOut > 0 && (cap.Truncated() || len(out) > t.maxOut) {
		if len(out) > t.maxOut {
			out = out[:t.maxOut]
		}
		out += fmt.Sprintf("\n... truncated at %d bytes", t.maxOut)
	}
	out = scrubSecrets(out)
	header := t.formatResultHeader(in.Argv, callCtx, runErr)
	if strings.TrimSpace(out) == "" {
		return header + "(no output)", nil
	}
	return header + out, nil
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

func (t *runCommandTool) formatResultHeader(argv []string, callCtx context.Context, runErr error) string {
	status := exitStatus(callCtx, runErr)
	argPart := FormatArgv(argv)
	if t.redactArgs || RedactToolArgs() {
		argPart = argv[0] + " [arguments redacted]"
	} else {
		argPart = scrubSecrets(argPart)
	}
	return fmt.Sprintf("command: %s\ncwd: %s\n%s\n", argPart, t.ws.Abs, status)
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
	for _, a := range t.allowlist {
		if a == bin || a == base {
			return true
		}
	}
	return false
}

func filterEnv(env []string) []string {
	// Allowlist of known-safe environment variable prefixes.
	// Variables not matching any prefix are dropped to prevent secret leakage
	// to child processes.
	var out []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if isAllowedEnvVar(key) {
			out = append(out, e)
		}
	}
	return out
}

// isAllowedEnvVar reports whether a variable key is safe to pass to subprocesses.
// Uses an allowlist approach to prevent secret leakage.
func isAllowedEnvVar(key string) bool {
	uk := strings.ToUpper(key)

	// Exact allowlist of essential POSIX variables.
	switch uk {
	case "PATH", "HOME", "USER", "USERNAME", "LOGNAME",
		"TMPDIR", "TMP", "TEMP",
		"SHELL", "TERM",
		"PWD", "OLDPWD",
		"HOSTNAME", "HOST",
		"LANG", "LANGUAGE",
		"EDITOR", "VISUAL",
		"MAKE", "MAKEFLAGS", "MAKELEVEL", "MFLAGS",
		"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
		"SSH_AUTH_SOCK", "SSH_AGENT_PID",
		"GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR",
		"NPM_CONFIG_USERCONFIG",
		"CARGO_HOME", "RUSTUP_HOME", "GOPATH", "GOROOT",
		"KUBECONFIG":
		return true
	}

	// Prefix-based allowlist for locale, XDG, and git variables.
	if strings.HasPrefix(uk, "LC_") || strings.HasPrefix(uk, "XDG_") ||
		strings.HasPrefix(uk, "GIT_") && !strings.HasPrefix(uk, "GIT_TOKEN") ||
		strings.HasPrefix(uk, "NODE_") && !strings.HasPrefix(uk, "NODE_OPTIONS") && !strings.HasPrefix(uk, "NODE_PRESERVE_SYMLINKS") {
		// Block known secrets within these namespaces.
		if strings.Contains(uk, "SECRET") || strings.Contains(uk, "TOKEN") || strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "API_KEY") {
			return false
		}
		return true
	}

	return false
}

func scrubSecrets(s string) string {
	// Lightweight scrub for common key prefixes in tool output.
	for _, prefix := range []string{"github_pat_", "sk-ant-", "ghp_", "sk-"} {
		for {
			i := strings.Index(s, prefix)
			if i < 0 {
				break
			}
			j := i + len(prefix)
			for j < len(s) && j < i+80 && isKeyChar(s[j]) {
				j++
			}
			// Replace the entire match with [redacted] (brackets ensure the
			// replacement never re-matches any prefix in subsequent iterations).
			// Resume search after the replacement to avoid re-matching.
			s = s[:i] + "[redacted]" + s[j:]
		}
	}
	return s
}

func isKeyChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}
