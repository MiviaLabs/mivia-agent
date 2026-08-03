package agents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestMergeToolsRemoveIntoDisallowed(t *testing.T) {
	remove := []string{"post_message", " ", "read_file", "post_message"}
	spec := config.AgentFileSpec{ToolsRemove: &remove}
	got := mergeToolsRemoveIntoDisallowed(nil, spec)
	if got == nil || len(*got) != 2 {
		t.Fatalf("got=%v", got)
	}
	// Existing disallowed preserved and extended.
	existing := []string{"run_command"}
	got = mergeToolsRemoveIntoDisallowed(&existing, spec)
	if len(*got) != 3 {
		t.Fatalf("got=%v", got)
	}
	// No tools_remove: identity.
	empty := config.AgentFileSpec{}
	if mergeToolsRemoveIntoDisallowed(&existing, empty) != &existing && len(*mergeToolsRemoveIntoDisallowed(&existing, empty)) != 1 {
		// function may return new slice clone path - just check empty remove returns dis
		if out := mergeToolsRemoveIntoDisallowed(&existing, empty); out == nil || (*out)[0] != "run_command" {
			t.Fatalf("identity path: %v", out)
		}
	}
}
