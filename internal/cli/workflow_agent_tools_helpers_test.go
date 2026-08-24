package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// setWorkflowAgentTools is duplicated from internal/clichat for the
// workflow hook integration test that stayed in this package.

// setWorkflowAgentTools writes both workflow agents with the given tool.
func setWorkflowAgentTools(t *testing.T, root, tool string) {
	t.Helper()
	for _, name := range []string{"one", "two"} {
		path := filepath.Join(root, ".agents", "agents", name+".md")
		body := "---\nname: " + name + "\ndescription: \"workflow agent\"\ntools:\n  - " + tool + "\nmax_turns: 2\n---\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
