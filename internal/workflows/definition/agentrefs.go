package definition

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ValidateAgentReferences checks that every step with kind "agent" or "agent_gate"
// references an agent that exists: a file in <workspaceRoot>/.agents/agents/
// or a compiled built-in. Returns errors for any referenced agent that is
// not found.
func ValidateAgentReferences(wf *WorkflowFile, workspaceRoot string) []string {
	var errs []string
	agentsDir := workspace.AgentsDir(workspaceRoot)

	knownAgents, err := discoverAgentFiles(agentsDir)
	if err != nil {
		return []string{err.Error()}
	}
	// Compiled built-ins ship with the binary; a workflow may reference them
	// with no file present.
	knownAgents[agents.BuiltInGeneralPurposeName] = true

	for _, s := range wf.Steps {
		if s.Kind == "agent" || s.Kind == "agent_gate" || s.Kind == "agent_panel" {
			if s.Agent != "" && !knownAgents[s.Agent] {
				errs = append(errs, fmt.Sprintf("step %q: agent %q not found in %s", s.ID, s.Agent, agentsDir))
			}
		}
		if s.Kind != "agent_panel" || s.Panel == nil {
			continue
		}
		for _, member := range s.Panel.Members {
			if !knownAgents[member.Agent] {
				errs = append(errs, fmt.Sprintf("step %q: panel member %q: agent %q not found in %s", s.ID, member.ID, member.Agent, agentsDir))
			}
		}
	}
	return errs
}

// ValidateAgentSkillReferences checks each selected workflow agent and skill
// against the resolved agent and skill catalogues. An empty skill is accepted
// for workflows admitted before explicit skill bindings existed.
func ValidateAgentSkillReferences(wf *CompiledWorkflow, agentRegistry *agents.AgentRegistry, skillRegistry *skills.Registry) []string {
	var errs []string
	if wf == nil {
		return []string{"compiled workflow is nil"}
	}
	if agentRegistry == nil {
		return []string{"workflow agent registry is nil"}
	}
	if skillRegistry == nil {
		return []string{"workflow skill registry is nil"}
	}
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" && step.Kind != "agent_panel" {
			continue
		}
		errs = append(errs, validateAgentSkillReference(step.ID, "", step.Agent, step.Skill, agentRegistry, skillRegistry)...)
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			errs = append(errs, validateAgentSkillReference(step.ID, member.ID, member.Agent, member.Skill, agentRegistry, skillRegistry)...)
		}
	}
	return errs
}

func validateAgentSkillReference(stepID, memberID, agentName, skillName string, agentRegistry *agents.AgentRegistry, skillRegistry *skills.Registry) []string {
	var errs []string
	prefix := fmt.Sprintf("step %q", stepID)
	if memberID != "" {
		prefix += fmt.Sprintf(": panel member %q", memberID)
	}
	agent, ok := agentRegistry.Get(agentName)
	if !ok {
		return []string{fmt.Sprintf("%s: agent %q not found", prefix, agentName)}
	}
	if skillName == "" {
		return nil
	}
	if _, ok := skillRegistry.Get(skillName); !ok {
		return []string{fmt.Sprintf("%s: skill %q not found", prefix, skillName)}
	}
	if !agents.SkillAllowed(&agent, skillName) {
		return []string{fmt.Sprintf("%s: agent %q may not use skill %q", prefix, agent.Name, skillName)}
	}
	return errs
}

// discoverAgentFiles reads the agents directory and returns a set of known agent names
// (file basenames without extension). Returns an empty set if the directory doesn't exist.
// Skips directories and symbolic links.
func discoverAgentFiles(agentsDir string) (map[string]bool, error) {
	known := make(map[string]bool)

	// A regular file where the agents directory should be must be an error,
	// not an empty directory. Windows reports the read of "<file>" as an
	// empty listing (FindFirstFile succeeds on a file), and a missing
	// "<file>/child" as ERROR_PATH_NOT_FOUND (os.IsNotExist), so only the
	// explicit stat distinguishes the two on that platform.
	if info, statErr := os.Stat(agentsDir); statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("reading agents directory %s: not a directory", agentsDir)
	}

	entries, err := os.ReadDir(agentsDir)
	if os.IsNotExist(err) {
		return known, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading agents directory %s: %w", agentsDir, err)
	}

	for _, entry := range entries {
		// Skip directories and symlinks
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			continue
		}

		name := entry.Name()
		ext := filepath.Ext(name)
		switch ext {
		case ".md", ".toml", ".yaml", ".yml":
			known[strings.TrimSuffix(name, ext)] = true
		}
	}
	return known, nil
}
