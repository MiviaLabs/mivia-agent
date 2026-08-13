package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestCoverageValidateAgentReferencesTable(t *testing.T) {
	two := func(a, b string) *definition.WorkflowFile {
		return &definition.WorkflowFile{
			Version: 1, Name: "two-step-agent-refs", InitialStep: "step1",
			Steps:       []definition.Step{{ID: "step1", Kind: "agent", Agent: a}, {ID: "step2", Kind: "agent", Agent: b}},
			Transitions: []definition.Transition{{From: "step1", To: "step2", Match: definition.MatchCriteria{Status: "succeeded"}}, {From: "step2", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}}},
		}
	}
	for _, tt := range []struct {
		name    string
		wf      *definition.WorkflowFile
		agents  []string
		wantErr []string
	}{
		{"step1 agent resolves", agentTestWorkflow("step1", "agent", "worker"), []string{"worker.md"}, nil},
		{"step2 missing agent fails closed naming step2", two("worker", "missing"), []string{"worker.md"}, []string{`step "step2"`, "not found"}},
		{"duplicate references to one known agent across two steps pass", two("worker", "worker"), []string{"worker.md"}, nil},
		{"empty agents catalog under workspace namespace fails closed", agentTestWorkflow("step1", "agent", "worker"), nil, []string{"not found"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := workspace.NamespacePath(root, "agents")
			wantSubstrings(t, os.MkdirAll(dir, 0o755), nil)
			for _, a := range tt.agents {
				wantSubstrings(t, os.WriteFile(filepath.Join(dir, a), []byte("# "+a), 0o644), nil)
			}
			wantSubstrings(t, ValidateAgentReferences(tt.wf, root), tt.wantErr)
		})
	}
}
func TestCoverageAgentGateSkillReferences(t *testing.T) {
	allowed := []string{"allowed"}
	agentRegistry := agents.NewRegistry()
	wantSubstrings(t, agentRegistry.Publish(agents.ResolvedAgent{Name: "worker", Skills: &allowed}), nil)
	skillRegistry := skills.NewRegistry()
	for _, s := range []string{"allowed", "denied"} {
		wantSubstrings(t, skillRegistry.Register(skills.Definition{Name: s}), nil)
	}
	for _, tt := range []struct {
		name, skill string
		wantErr     []string
	}{
		{"allowed skill passes", "allowed", nil},
		{"unknown skill fails closed", "unknown", []string{`skill "unknown" not found`}},
		{"skill not allowed fails closed", "denied", []string{`may not use skill "denied"`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wf := agentTestWorkflow("step1", "agent_gate", "worker")
			wf.Steps[0].Skill = tt.skill
			compiled := covCompile(t, wf)
			wantSubstrings(t, ValidateAgentSkillReferences(compiled, agentRegistry, skillRegistry), tt.wantErr)
		})
	}
}
