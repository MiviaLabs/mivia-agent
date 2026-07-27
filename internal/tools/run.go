package tools

import (
	"bytes"
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
	return "LAST RESORT: run an allowlisted program as argv (no shell). Prefer read_file, list_dir, grep, glob, write_file, and search_replace for file work. Use for project tests, builds, package managers, and version control only when those tools cannot help. argv is a string array, e.g. [\"make\",\"test\"] or [\"npm\",\"test\"]."
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var runErr error
	if err := cmd.Start(); err != nil {
		runErr = err
	} else if err := scope.attach(cmd); err != nil {
		_ = scope.cancel(cmd)
		_ = cmd.Wait()
		runErr = err
	} else {
		runErr = cmd.Wait()
	}

	out := stdout.String() + stderr.String()
	if len(out) > t.maxOut {
		out = out[:t.maxOut] + fmt.Sprintf("\n... truncated at %d bytes", t.maxOut)
	}
	out = scrubSecrets(out)

	status := "exit=0"
	if runErr != nil {
		switch {
		case callCtx.Err() == context.DeadlineExceeded:
			status = "exit=timeout"
		case callCtx.Err() == context.Canceled:
			// Parent/session cancel must be model-visible (not a vague exit=error).
			status = "exit=canceled"
		default:
			if ee, ok := runErr.(*exec.ExitError); ok {
				status = fmt.Sprintf("exit=%d", ee.ExitCode())
			} else {
				status = "exit=error"
			}
		}
	}
	// Do not echo model-controlled arguments into the model/UI/trace output.
	// Arguments can contain secrets or personal data even when stdout is clean.
	header := fmt.Sprintf("command: %s [arguments redacted]\ncwd: %s\n%s\n", in.Argv[0], t.ws.Abs, status)
	// Always return nil error with exit status in the body so the model can
	// observe failures. Tool transport errors (allowlist, path) still error.
	if strings.TrimSpace(out) == "" {
		return header + "(no output)", nil
	}
	return header + out, nil
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
	var out []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		uk := strings.ToUpper(key)
		if strings.Contains(uk, "API_KEY") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "TOKEN") || strings.Contains(uk, "PASSWORD") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func scrubSecrets(s string) string {
	// Lightweight scrub for common key prefixes in tool output.
	for _, prefix := range []string{"sk-", "sk-ant-", "ghp_", "github_pat_"} {
		for {
			i := strings.Index(s, prefix)
			if i < 0 {
				break
			}
			j := i + len(prefix)
			for j < len(s) && j < i+80 && isKeyChar(s[j]) {
				j++
			}
			s = s[:i] + prefix + "REDACTED" + s[j:]
		}
	}
	return s
}

func isKeyChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}
