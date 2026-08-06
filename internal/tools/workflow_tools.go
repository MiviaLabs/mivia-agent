package tools

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// WorkflowToolsBuilder constructs Phase 7 workflow tools for a workspace.
// The tools package must not import workflow packages (ledger/storage) or it
// creates a test import cycle with internal/storage. CLI (and tests) set the
// builder via SetWorkflowToolsBuilder.
type WorkflowToolsBuilder func(opts DefaultOptions) []Tool

var (
	workflowToolsMu      sync.RWMutex
	workflowToolsBuilder WorkflowToolsBuilder
)

// SetWorkflowToolsBuilder installs the factory that registers workflow tools
// when a workspace has .mivia/workflows/. Pass nil to clear.
func SetWorkflowToolsBuilder(b WorkflowToolsBuilder) {
	workflowToolsMu.Lock()
	workflowToolsBuilder = b
	workflowToolsMu.Unlock()
}

func getWorkflowToolsBuilder() WorkflowToolsBuilder {
	workflowToolsMu.RLock()
	defer workflowToolsMu.RUnlock()
	return workflowToolsBuilder
}

// HasWorkflowsDir reports whether root contains .mivia/workflows/.
func HasWorkflowsDir(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, ".mivia", "workflows"))
	return err == nil && info.IsDir()
}

// registerWorkflowTools registers Phase 7 workflow tools when the workspace
// has .mivia/workflows/ and a builder is installed.
func registerWorkflowTools(register func(Tool), opts DefaultOptions) {
	root := ""
	if opts.Workspace != nil {
		root = opts.Workspace.Abs
	}
	if !HasWorkflowsDir(root) {
		return
	}
	builder := getWorkflowToolsBuilder()
	if builder == nil {
		// No builder: still allow pre-built tools from DefaultOptions.
		for _, tool := range opts.WorkflowTools {
			if tool != nil {
				register(tool)
			}
		}
		return
	}
	for _, tool := range builder(opts) {
		if tool != nil {
			register(tool)
		}
	}
}

// WorkflowTools, when set, are registered if a builder is not installed and
// the workspace has .mivia/workflows/. Prefer SetWorkflowToolsBuilder for
// production wiring.
//
// DefaultOptions field is declared in default_registry.go.
func workflowWorkspaceRoot(ws *workspace.Root) string {
	if ws == nil {
		return ""
	}
	return ws.Abs
}
