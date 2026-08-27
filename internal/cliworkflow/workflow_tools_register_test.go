package cliworkflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestWorkflowToolsDefaultInDefaultRegistry pins that workflow tools are
// default for agents: a plain default registry built over a workspace with
// .mivia/workflows/ (no session pre-wiring) must register all seven tools,
// and the read tools must execute against the workspace ledger instead of
// failing with ErrRepoUnset.
func TestWorkflowToolsDefaultInDefaultRegistry(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	for _, name := range ledger.AllToolNames() {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("expected %s registered by default when the workspace has .mivia/workflows/", name)
		}
	}
	// The read tools must be functional, not UnsetRepoFactory stubs: an
	// empty workspace must list zero runs, not report an unconfigured ledger.
	tool, ok := reg.Get(ledger.ToolWorkflowListRuns)
	if !ok {
		t.Fatal("workflow_list_runs is not registered")
	}
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("workflow_list_runs must execute against the workspace ledger by default: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("workflow_list_runs returned empty output")
	}
}

// TestWorkflowToolsDefaultAbsentWithoutWorkflowsDir pins that the default
// registry still excludes workflow tools for workspaces without workflows.
func TestWorkflowToolsDefaultAbsentWithoutWorkflowsDir(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	for _, name := range ledger.AllToolNames() {
		if _, ok := reg.Get(name); ok {
			t.Errorf("did not expect %s without .mivia/workflows/", name)
		}
	}
}
