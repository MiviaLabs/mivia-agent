package composition

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// stubTool is a minimal tools.Tool used to prove a hook-blocked call never
// reaches the handler.
type stubTool struct {
	ran bool
}

func (s *stubTool) Name() string               { return "stub_tool" }
func (s *stubTool) Description() string        { return "test-only stub" }
func (s *stubTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	s.ran = true
	return `{"ok":true}`, nil
}

// blockingPreToolUseGroups builds a single PreToolUse hook group whose
// handler is a script on disk (at dir/guard.sh) that exits 2, the hooks
// protocol's block verdict.
func blockingPreToolUseGroups(t *testing.T, dir string) []hooks.Group {
	t.Helper()
	if os.PathSeparator != '/' {
		t.Skip("POSIX script fixture")
	}
	script := filepath.Join(dir, "guard.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'policy forbids this call\\n' >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatalf("write guard.sh: %v", err)
	}
	groups, err := hooks.Parse([]byte(`[[hooks]]
event = "PreToolUse"

  [[hooks.handlers]]
  type = "command"
  argv = ["./guard.sh"]
`), filepath.Join(dir, "mivia.toml"))
	if err != nil {
		t.Fatalf("hooks.Parse: %v", err)
	}
	return groups
}

// TestBuildDispatcher_HookBlocksTool exercises BuildDispatcher end to end: a
// stub tool registered on the registry BuildDispatcher wraps, a PreToolUse
// hook that returns a block verdict, invoked through the dispatcher
// BuildDispatcher returned. The tool must not run, the error text must carry
// the hook's reason, and the invocation's HookRuns must record the denial.
func TestBuildDispatcher_HookBlocksTool(t *testing.T) {
	dir := t.TempDir()
	groups := blockingPreToolUseGroups(t, dir)

	reg := tools.NewRegistry()
	tool := &stubTool{}
	reg.Register(tool)

	d, err := BuildDispatcher(DispatcherInput{
		Registry:        reg,
		WorkspaceRoot:   dir,
		HooksConfigured: true,
		HookGroups:      func() []hooks.Group { return groups },
	})
	if err != nil {
		t.Fatalf("BuildDispatcher: %v", err)
	}

	result := d.Invoke(context.Background(), runtime.Request{
		ID:   "call-1",
		Kind: runtime.Tool,
		Name: tool.Name(),
	})

	if tool.ran {
		t.Fatal("a hook-blocked call must not reach the tool")
	}
	if result.Metadata.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", result.Metadata.Status)
	}
	if got := string(result.Output); !strings.Contains(got, "policy forbids this call") {
		t.Fatalf("the hook's reason must reach the caller, got %s", got)
	}
	if len(result.HookRuns) != 1 || !result.HookRuns[0].Denied {
		t.Fatalf("the blocking run must be recorded as denied: %+v", result.HookRuns)
	}
}

// TestBuildDispatcher_NoHooksConfigured pins the nil-hook-funcs contract:
// with HooksConfigured false, the built dispatcher runs the tool exactly as
// it would with no hooks at all.
func TestBuildDispatcher_NoHooksConfigured(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &stubTool{}
	reg.Register(tool)

	d, err := BuildDispatcher(DispatcherInput{Registry: reg})
	if err != nil {
		t.Fatalf("BuildDispatcher: %v", err)
	}

	result := d.Invoke(context.Background(), runtime.Request{ID: "call-1", Kind: runtime.Tool, Name: tool.Name()})
	if !tool.ran {
		t.Fatal("the tool must run when no hooks are configured")
	}
	if result.Metadata.Status != "completed" {
		t.Fatalf("status = %q, want completed", result.Metadata.Status)
	}
}

// TestBuildDispatcher_HooksConfiguredButNoGroups covers the case where
// HooksConfigured is true (so the policy funcs are installed) but HookGroups
// returns nothing at call time - the session's hooks were unarmed between
// build and invocation. The tool must still run and no run must be recorded.
func TestBuildDispatcher_HooksConfiguredButNoGroups(t *testing.T) {
	reg := tools.NewRegistry()
	tool := &stubTool{}
	reg.Register(tool)
	var noted [][]string

	d, err := BuildDispatcher(DispatcherInput{
		Registry:         reg,
		WorkspaceRoot:    t.TempDir(),
		HooksConfigured:  true,
		HookGroups:       func() []hooks.Group { return nil },
		NoteHookWarnings: func(w []string) { noted = append(noted, w) },
	})
	if err != nil {
		t.Fatalf("BuildDispatcher: %v", err)
	}

	result := d.Invoke(context.Background(), runtime.Request{ID: "call-1", Kind: runtime.Tool, Name: tool.Name()})
	if !tool.ran {
		t.Fatal("the tool must run when no hook groups are armed")
	}
	if len(result.HookRuns) != 0 {
		t.Fatalf("no hook groups means no runs, got %+v", result.HookRuns)
	}
	if len(noted) != 0 {
		t.Fatalf("no hook groups means no warnings to note, got %+v", noted)
	}
}

// TestHookFileFromInput covers hookFileFromInput's three branches: empty
// input, a "path" field, and input that fails to unmarshal.
func TestHookFileFromInput(t *testing.T) {
	cases := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{"empty input", nil, ""},
		{"path present", json.RawMessage(`{"path":"a/b.go"}`), "a/b.go"},
		{"malformed json", json.RawMessage(`{`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hookFileFromInput(c.input); got != c.want {
				t.Fatalf("hookFileFromInput(%s) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}
