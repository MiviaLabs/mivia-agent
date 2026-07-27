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
	return Capability{Class: ExecutionExternal}
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
	if len(in.Argv) == 0 {
		return "", fmt.Errorf("argv must be non-empty")
	}
	bin := in.Argv[0]
	// Reject path separators in binary name — only bare names from allowlist/PATH.
	if strings.Contains(bin, string(os.PathSeparator)) || strings.Contains(bin, "/") || strings.Contains(bin, "\\") {
		return "", fmt.Errorf("program must be a bare name on the allowlist, not a path: %q", bin)
	}
	if !t.allowed(bin) {
		return "", fmt.Errorf("program %q is not allowlisted (allowed: %s)", bin, strings.Join(t.allowlist, ", "))
	}
	// Resolve binary from PATH only.
	resolved, commandArgs := bin, in.Argv[1:]
	if runtime.GOOS == "windows" && (bin == "echo" || bin == "true" || bin == "false") {
		resolved = os.Getenv("ComSpec")
		if resolved == "" {
			resolved = "cmd.exe"
		}
		commandArgs = append([]string{"/d", "/c", bin}, commandArgs...)
	} else {
		var lookErr error
		resolved, lookErr = exec.LookPath(bin)
		if lookErr != nil {
			return "", fmt.Errorf("program not found on PATH: %s", bin)
		}
	}

	timeout := time.Duration(t.timeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, resolved, commandArgs...)
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
		if ctx.Err() == context.DeadlineExceeded {
			status = "exit=timeout"
		} else if ee, ok := runErr.(*exec.ExitError); ok {
			status = fmt.Sprintf("exit=%d", ee.ExitCode())
		} else {
			status = "exit=error"
		}
	}
	header := fmt.Sprintf("command: %s\ncwd: %s\n%s\n", strings.Join(in.Argv, " "), t.ws.Abs, status)
	// Always return nil error with exit status in the body so the model can
	// observe failures. Tool transport errors (allowlist, path) still error.
	if strings.TrimSpace(out) == "" {
		return header + "(no output)", nil
	}
	return header + out, nil
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
