package cliagents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/composition"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestWorktreeSurfaceHooksUseEntryRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	launch, worktree := t.TempDir(), t.TempDir()
	ws, err := workspace.Open(worktree)
	if err != nil {
		t.Fatal(err)
	}
	base := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = base
	sess.ToolBaseResolver = func() *tools.Registry { return base }
	state := &AgentSessionState{WorkspaceRoot: launch, ToolBase: tools.NewRegistry(), SkillRegFull: skills.NewRegistry()}
	previous := NewSessionDispatcherVar
	t.Cleanup(func() { NewSessionDispatcherVar = previous })
	var gotRoot string
	NewSessionDispatcherVar = func(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		gotRoot = opts.WorkspaceRoot
		return nil, nil
	}
	for _, path := range []string{"agent", "admission", "model", "unscoped_model"} {
		t.Run(path, func(t *testing.T) {
			var err error
			switch path {
			case "agent":
				_, err = buildAgentScopedSurface(sess, res, state, nil)
			case "admission":
				_, err = buildWidenedWith(sess, res, state, nil)
			case "model":
				_, err = modelSwitchSurface(sess, res, state, sess.CurrentBinding(), skills.NewRegistry())
			case "unscoped_model":
				_, err = unscopedModelSurface(sess, res, launch, sess.CurrentBinding(), base, 0, state.Context(), skills.NewRegistry(), nil, nil, config.MemoryConfig{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotRoot != worktree {
				t.Fatalf("hook root = %q, want active worktree %q", gotRoot, worktree)
			}
		})
	}
}

func TestWorktreeHookExecutesBesideFileTools(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("POSIX hook fixture")
	}
	launch, worktree := t.TempDir(), t.TempDir()
	script := filepath.Join(launch, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$PWD\" > hook-cwd\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	groups, err := hooks.Parse([]byte("[[hooks]]\nevent = 'PreToolUse'\n[[hooks.handlers]]\ntype = 'command'\nargv = ['./hook.sh']\n"), filepath.Join(launch, "mivia.toml"))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(worktree)
	if err != nil {
		t.Fatal(err)
	}
	base := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = base
	sess.ToolBaseResolver = func() *tools.Registry { return base }
	state := &AgentSessionState{WorkspaceRoot: launch, ToolBase: base, SkillRegFull: skills.NewRegistry()}
	previous := NewSessionDispatcherVar
	t.Cleanup(func() { NewSessionDispatcherVar = previous })
	NewSessionDispatcherVar = func(opts SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		return composition.BuildDispatcher(composition.DispatcherInput{
			Registry: opts.Registry, WorkspaceRoot: opts.WorkspaceRoot,
			HooksConfigured: true, HookGroups: func() []hooks.Group { return groups },
		})
	}
	surface, err := buildWidenedWith(sess, res, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer surface.dispatcher.Close()
	result := surface.dispatcher.Invoke(context.Background(), runtime.Request{
		ID: "read-hook-cwd", Kind: runtime.Tool, Name: "read_file", Input: []byte(`{"path":"hook-cwd"}`),
	})
	if !strings.Contains(string(result.Output), worktree) {
		t.Fatalf("file tools and hook cwd disagree: %s", result.Output)
	}
	if _, err := os.Stat(filepath.Join(launch, "hook-cwd")); !os.IsNotExist(err) {
		t.Fatalf("hook touched launch checkout: %v", err)
	}
}
