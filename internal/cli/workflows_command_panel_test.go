package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestValidateWorkflowFiles_AgentPanel(t *testing.T) {
	wf := &compiler.CompiledWorkflow{
		Steps: []definition.Step{{
			ID:   "review",
			Kind: "agent_panel",
			Panel: &definition.AgentPanel{Members: []definition.PanelMember{{
				ID:       "security",
				Template: "templates/missing.md",
			}}},
		}},
	}
	err := validateWorkflowFiles(t.TempDir(), wf)
	if err == nil || !strings.Contains(err.Error(), `panel member "security": template "templates/missing.md":`) {
		t.Fatalf("validateWorkflowFiles() error = %v", err)
	}
}

func TestValidateWorkflowSkillTools_AgentPanelMember(t *testing.T) {
	allowed := []string{"review"}
	registry := agents.NewRegistry()
	if err := registry.Publish(agents.ResolvedAgent{Name: "panel-reviewer", Skills: &allowed}); err != nil {
		t.Fatal(err)
	}
	skillRegistry := skills.NewRegistry()
	if err := skillRegistry.Register(skills.Definition{Name: "review", Tools: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	wf := &compiler.CompiledWorkflow{Steps: []definition.Step{{
		ID:    "review",
		Kind:  "agent_panel",
		Panel: &definition.AgentPanel{Members: []definition.PanelMember{{ID: "security", Agent: "panel-reviewer", Skill: "review"}}},
	}}}
	err := validateWorkflowSkillTools(wf, registry, skillRegistry)
	if err == nil || !strings.Contains(err.Error(), `panel member "security"`) {
		t.Fatalf("validateWorkflowSkillTools() error = %v", err)
	}
}
