package agenttools

import (
	"os"
	"path/filepath"
)

// WorkflowsDir is the workspace-relative path that holds workflow TOML files.
const WorkflowsDir = ".mivia/workflows"

// HasWorkflows reports whether root contains a workflows directory that can
// hold workflow definitions. The check is presence-only: an empty directory
// still enables tool registration so authors can add workflows later in the
// same session without restarting.
func HasWorkflows(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, WorkflowsDir))
	return err == nil && info.IsDir()
}
