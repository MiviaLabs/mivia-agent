package compiler

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ValidateAgentReferences checks that every step with kind "agent" or "agent_gate"
// references an agent file that exists in <workspaceRoot>/.mivia/agents/.
// Returns an error if any referenced agent is not found.
func ValidateAgentReferences(wf *definition.WorkflowFile, workspaceRoot string) error {
	agentsDir := workspace.NamespacePath(workspaceRoot, "agents")

	knownAgents, err := discoverAgentFiles(agentsDir)
	if err != nil {
		return err
	}

	for _, s := range wf.Steps {
		if s.Kind == "agent" || s.Kind == "agent_gate" || s.Kind == "agent_panel" {
			if s.Agent != "" && !knownAgents[s.Agent] {
				return fmt.Errorf("step %q: agent %q not found in %s", s.ID, s.Agent, agentsDir)
			}
		}
		if s.Kind != "agent_panel" || s.Panel == nil {
			continue
		}
		for _, member := range s.Panel.Members {
			if !knownAgents[member.Agent] {
				return fmt.Errorf("step %q: panel member %q: agent %q not found in %s", s.ID, member.ID, member.Agent, agentsDir)
			}
		}
	}
	return nil
}

// ValidateAgentSkillReferences checks each selected workflow agent and skill
// against the resolved agent and skill catalogues. An empty skill is accepted
// for workflows admitted before explicit skill bindings existed.
func ValidateAgentSkillReferences(wf *CompiledWorkflow, agentRegistry *agents.AgentRegistry, skillRegistry *skills.Registry) error {
	if wf == nil {
		return fmt.Errorf("compiled workflow is nil")
	}
	if agentRegistry == nil {
		return fmt.Errorf("workflow agent registry is nil")
	}
	if skillRegistry == nil {
		return fmt.Errorf("workflow skill registry is nil")
	}
	for _, step := range wf.Steps {
		if step.Kind != "agent" && step.Kind != "agent_gate" && step.Kind != "agent_panel" {
			continue
		}
		if err := validateAgentSkillReference(step.ID, "", step.Agent, step.Skill, agentRegistry, skillRegistry); err != nil {
			return err
		}
		if step.Kind != "agent_panel" || step.Panel == nil {
			continue
		}
		for _, member := range step.Panel.Members {
			if err := validateAgentSkillReference(step.ID, member.ID, member.Agent, member.Skill, agentRegistry, skillRegistry); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAgentSkillReference(stepID, memberID, agentName, skillName string, agentRegistry *agents.AgentRegistry, skillRegistry *skills.Registry) error {
	prefix := fmt.Sprintf("step %q", stepID)
	if memberID != "" {
		prefix += fmt.Sprintf(": panel member %q", memberID)
	}
	agent, ok := agentRegistry.Get(agentName)
	if !ok {
		return fmt.Errorf("%s: agent %q not found", prefix, agentName)
	}
	if skillName == "" {
		return nil
	}
	if _, ok := skillRegistry.Get(skillName); !ok {
		return fmt.Errorf("%s: skill %q not found", prefix, skillName)
	}
	if !agents.SkillAllowed(&agent, skillName) {
		return fmt.Errorf("%s: agent %q may not use skill %q", prefix, agent.Name, skillName)
	}
	return nil
}

// discoverAgentFiles reads the agents directory and returns a set of known agent names
// (file basenames without extension). Returns an empty set if the directory doesn't exist.
// Skips directories and symbolic links.
func discoverAgentFiles(agentsDir string) (map[string]bool, error) {
	known := make(map[string]bool)

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
