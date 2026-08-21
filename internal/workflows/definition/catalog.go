package definition

import (
	"fmt"
	"strings"
)

// FormatWorkflowList formats a list of discovered workflow names for CLI output.
func FormatWorkflowList(workflows []DiscoveredWorkflow) string {
	if len(workflows) == 0 {
		return "No workflows found.\n"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Workflows (%d):\n", len(workflows)))
	for _, wf := range workflows {
		b.WriteString(fmt.Sprintf("  %s\n", wf.Name))
	}
	return b.String()
}
