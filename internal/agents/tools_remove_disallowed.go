package agents

import (
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// mergeToolsRemoveIntoDisallowed promotes tools_remove entries into the
// denylist so baseline capabilities (e.g. post_message inject) honor the
// same opt-out surface as disallowed_tools (plan 53.02).
func mergeToolsRemoveIntoDisallowed(dis *[]string, spec config.AgentFileSpec) *[]string {
	if spec.ToolsRemove == nil || len(*spec.ToolsRemove) == 0 {
		return dis
	}
	var base []string
	if dis != nil {
		base = slices.Clone(*dis)
	}
	seen := map[string]bool{}
	for _, n := range base {
		seen[n] = true
	}
	for _, n := range *spec.ToolsRemove {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		base = append(base, n)
	}
	return &base
}
