package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func installTestWorkflowBuilder(t *testing.T) {
	t.Helper()
	tools.SetWorkflowToolsBuilder(func(opts tools.DefaultOptions) []tools.Tool {
		root := ""
		if opts.Workspace != nil {
			root = opts.Workspace.Abs
		}
		if !ledger.HasWorkflows(root) {
			return nil
		}
		svc, err := ledger.NewService(ledger.ServiceOptions{
			Repo: ledger.UnsetRepoFactory,
		})
		if err != nil {
			t.Fatal(err)
		}
		out := make([]tools.Tool, 0, 8)
		for _, inner := range ledger.Tools(svc) {
			out = append(out, &testWorkflowTool{inner: inner})
		}
		return out
	})
	t.Cleanup(func() { tools.SetWorkflowToolsBuilder(nil) })
}

type testWorkflowTool struct {
	inner ledger.Tool
}

func (t *testWorkflowTool) Name() string               { return t.inner.Name() }
func (t *testWorkflowTool) Description() string        { return t.inner.Description() }
func (t *testWorkflowTool) Parameters() map[string]any { return t.inner.Parameters() }
func (t *testWorkflowTool) ResultBudgetBytes() int     { return t.inner.ResultBudgetBytes() }
func (t *testWorkflowTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.inner.Execute(ctx, args)
}

func TestWorkflowToolsRegisteredWhenWorkflowsDirExists(t *testing.T) {
	installTestWorkflowBuilder(t)
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
			t.Errorf("expected %s registered when workflows dir exists", name)
		}
	}
}

func TestWorkflowToolsAbsentWithoutWorkflowsDir(t *testing.T) {
	installTestWorkflowBuilder(t)
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	for _, name := range ledger.AllToolNames() {
		if _, ok := reg.Get(name); ok {
			t.Errorf("did not expect %s without workflows dir", name)
		}
	}
}

func TestWorkflowToolSurfaceIsGeneric(t *testing.T) {
	installTestWorkflowBuilder(t)
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
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if tool.Description() == "" {
			t.Errorf("%s empty description", name)
		}
		if budget, ok := tool.(tools.ResultBudgetTool); ok {
			if budget.ResultBudgetBytes() <= 0 {
				t.Errorf("%s missing result budget", name)
			}
		} else {
			t.Errorf("%s must declare ResultBudgetBytes", name)
		}
	}
}

func TestHasWorkflowsDir(t *testing.T) {
	dir := t.TempDir()
	if tools.HasWorkflowsDir(dir) {
		t.Fatal("empty workspace must not report workflows")
	}
	if err := os.MkdirAll(filepath.Join(dir, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !tools.HasWorkflowsDir(dir) {
		t.Fatal("expected workflows dir")
	}
}
