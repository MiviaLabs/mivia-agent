package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// agentTestWorkflow returns a minimal WorkflowFile for agent reference tests.
func agentTestWorkflow(stepID, kind, agent string) *definition.WorkflowFile {
	return &definition.WorkflowFile{
		Version:     1,
		Name:        "test-agent-refs",
		Description: "test",
		InitialStep: stepID,
		Steps: []definition.Step{
			{ID: stepID, Kind: kind, Agent: agent},
		},
		Transitions: []definition.Transition{
			{From: stepID, To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
}

func TestValidateAgentReferences_NoAgentsDir(t *testing.T) {
	// Create a temp workspace with no .mivia/agents/ directory
	tmpDir := t.TempDir()

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	err := ValidateAgentReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error when .mivia/agents/ doesn't exist and step references agent")
	}
	if err.Error() != `step "do-thing": agent "worker" not found in `+filepath.Join(tmpDir, ".mivia", "agents") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateAgentReferences_AgentFound(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("# worker"), 0644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	err := ValidateAgentReferences(wf, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAgentReferences_AgentNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "other.md"), []byte("# other"), 0644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	err := ValidateAgentReferences(wf, tmpDir)
	if err == nil {
		t.Fatal("expected error when agent 'worker' is not in .mivia/agents/")
	}
	expected := `step "do-thing": agent "worker" not found in ` + agentsDir
	if err.Error() != expected {
		t.Errorf("error = %q, want %q", err.Error(), expected)
	}
}

func TestValidateAgentReferences_NonAgentStepSkipped(t *testing.T) {
	// Step kind "human_gate" with agent field — agent field should be ignored
	tmpDir := t.TempDir()

	wf := agentTestWorkflow("review", "human_gate", "some-agent")
	err := ValidateAgentReferences(wf, tmpDir)
	if err != nil {
		t.Fatalf("expected no error for non-agent step with agent field: %v", err)
	}
}

func TestValidateAgentReferences_YamlExtension(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "reviewer.yaml"), []byte("name: reviewer"), 0644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := agentTestWorkflow("do-review", "agent_gate", "reviewer")
	err := ValidateAgentReferences(wf, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
