package legacytui

import (
	"context"
	"encoding/json"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// newTestSessionForModel builds a minimal chat.Session for TUI model tests.
// Package-local copy of internal/cli's helper of the same name: Go test
// files are not part of a package's importable surface, so a helper shared
// by tests in both packages must exist in each; internal/cli's staying
// tests (composer_test.go, tui_phase1_test.go, and others) still need their
// own copy, so the cli original was not moved.
func newTestSessionForModel(model string) *chat.Session {
	return chat.NewSession(&config.Resolved{Model: model}, nil)
}

// namedTool and tierRegistry are package-local copies of internal/cli's
// helpers of the same name (agent_integration_test.go, tool_tiers_test.go):
// a minimal fake tools.Tool and a registry built from a list of tool names.
type namedTool struct{ name string }

func (t namedTool) Name() string               { return t.name }
func (t namedTool) Description() string        { return t.name }
func (t namedTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func tierRegistry(names ...string) *tools.Registry {
	reg := tools.NewRegistry()
	for _, name := range names {
		reg.Register(namedTool{name: name})
	}
	return reg
}

// nullCompleter is a package-local copy of internal/cli's helper of the same
// name (session_tool_surface_test.go): a stub provider.Completer for test
// dispatchers.
type nullCompleter struct{}

func (n nullCompleter) Name() string { return "null" }
func (n nullCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "", nil
}
func (n nullCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "", nil
}
func (n nullCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

// stubWorkspaceRestart is a package-local copy of internal/cli's helper of
// the same name (session_test_helpers_test.go): satisfies
// workspaceRestartError without importing the concrete cli.WorkspaceRestart
// type.
type stubWorkspaceRestart struct {
	dir, resumeSessionName string
	wt                     contextstate.WorktreeInstance
}

func (s stubWorkspaceRestart) Error() string { return "restart chat in workspace " + s.dir }

func (s stubWorkspaceRestart) WorkspaceRestartInfo() (string, string, contextstate.WorktreeInstance) {
	return s.dir, s.resumeSessionName, s.wt
}
