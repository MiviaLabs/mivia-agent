package agents

import (
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// resolveSkillsAllowlist applies plan 06 nil/empty/explicit semantics and trust.
// nil → unrestricted (all trusted skills); empty → none; names → validated set.
func resolveSkillsAllowlist(agentName string, skills *[]string, opts ResolveOptions) (*[]string, map[string]string, error) {
	if skills == nil {
		return nil, nil, nil
	}
	if len(*skills) == 0 {
		empty := []string{}
		return &empty, map[string]string{}, nil
	}
	// When a catalogue is provided, every name must resolve to a trusted origin.
	// Without a catalogue (unit tests that only care about tool inheritance),
	// accept names as-is so existing resolve tests stay focused.
	out := make([]string, 0, len(*skills))
	origins := make(map[string]string, len(*skills))
	seen := make(map[string]struct{}, len(*skills))
	for _, raw := range *skills {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, nil, fmt.Errorf("agent %q: skills entry must not be empty", agentName)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if opts.SkillCatalogue != nil {
			entry, ok := opts.SkillCatalogue[name]
			if !ok || (!entry.User && !entry.Project) {
				return nil, nil, fmt.Errorf("agent %q: unknown skill %q", agentName, name)
			}
			origin, err := pickSkillOrigin(agentName, name, entry, opts.AllowProjectSkills)
			if err != nil {
				return nil, nil, err
			}
			origins[name] = origin
		}
		out = append(out, name)
	}
	return &out, origins, nil
}

// pickSkillOrigin prefers user skills over project so a workspace skill cannot
// silently shadow a user binding. Project-only skills require the workspace gate.
func pickSkillOrigin(agentName, skillName string, entry SkillCatalogueEntry, allowProject bool) (string, error) {
	if entry.User {
		return string(config.AgentSourceUser), nil
	}
	if entry.Project {
		if !allowProject {
			return "", fmt.Errorf("agent %q: skill %q is workspace-only; enable load_workspace_config to use project skills", agentName, skillName)
		}
		return string(config.AgentSourceWorkspace), nil
	}
	return "", fmt.Errorf("agent %q: unknown skill %q", agentName, skillName)
}
