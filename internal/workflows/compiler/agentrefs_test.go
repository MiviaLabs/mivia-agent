package compiler

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
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
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error when .mivia/agents/ doesn't exist and step references agent")
	}
	if strings.Join(errs, "; ") != `step "do-thing": agent "worker" not found in `+filepath.Join(tmpDir, ".mivia", "agents") {
		t.Errorf("unexpected error: %v", errs)
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
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("unexpected error: %v", errs)
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
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error when agent 'worker' is not in .mivia/agents/")
	}
	expected := `step "do-thing": agent "worker" not found in ` + agentsDir
	if strings.Join(errs, "; ") != expected {
		t.Errorf("error = %q, want %q", strings.Join(errs, "; "), expected)
	}
}

func TestValidateAgentReferences_NonAgentStepSkipped(t *testing.T) {
	// Step kind "human_gate" with agent field — agent field should be ignored
	tmpDir := t.TempDir()

	wf := agentTestWorkflow("review", "human_gate", "some-agent")
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("expected no error for non-agent step with agent field: %v", errs)
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
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("unexpected error: %v", errs)
	}
}

func TestValidateAgentReferences_TOMLExtension(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.toml"), []byte("name = \"worker\""), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := ValidateAgentReferences(agentTestWorkflow("do-work", "agent", "worker"), tmpDir); len(errs) > 0 {
		t.Fatalf("toml agent reference: %v", errs)
	}
}

func TestValidateAgentReferences_AgentsDirIsFile(t *testing.T) {
	// When .mivia/agents is a regular file, os.ReadDir fails with an error
	// that is not IsNotExist, so discoverAgentFiles propagates it.
	tmpDir := t.TempDir()
	agentsPath := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatalf("creating .mivia dir: %v", err)
	}
	if err := os.WriteFile(agentsPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("creating agents file: %v", err)
	}

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected error when .mivia/agents is a regular file")
	}
	if !strings.Contains(strings.Join(errs, "; "), "reading agents directory") {
		t.Errorf("error %q should mention reading agents directory", strings.Join(errs, "; "))
	}
}

func TestValidateAgentReferences_SkipsEmptyAgentField(t *testing.T) {
	// A step with kind "agent" but an empty agent field is skipped;
	// the next step's agent is validated normally.
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("# worker"), 0o644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := &definition.WorkflowFile{
		Version:     1,
		Name:        "test-agent-refs",
		InitialStep: "first",
		Steps: []definition.Step{
			{ID: "first", Kind: "agent", Agent: ""},
			{ID: "second", Kind: "agent", Agent: "worker"},
		},
		Transitions: []definition.Transition{
			{From: "first", To: "second", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "second", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	if errs := ValidateAgentReferences(wf, tmpDir); len(errs) > 0 {
		t.Fatalf("expected no error: %v", errs)
	}
}

func TestValidateAgentReferences_SkipsSubdirectories(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(filepath.Join(agentsDir, "team"), 0o755); err != nil {
		t.Fatalf("creating agents subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("# worker"), 0o644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("unexpected error: %v", errs)
	}
}

func TestValidateAgentReferences_SkipsSymlinks(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(tmpDir, "nonexistent-target"), filepath.Join(agentsDir, "link.md")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("# worker"), 0o644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) > 0 {
		t.Fatalf("unexpected error: %v", errs)
	}
}

func TestValidateAgentReferences_InfoErrorSkipsEntry(t *testing.T) {
	// A read-only agents directory (0o400, read but no search) lets os.ReadDir
	// list entries but makes entry.Info() fail, so every entry is skipped and
	// the referenced agent is reported as missing.
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits, so a directory cannot be made unsearchable")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "worker.md"), []byte("# worker"), 0o644); err != nil {
		t.Fatalf("creating agent file: %v", err)
	}
	if err := os.Chmod(agentsDir, 0o400); err != nil {
		t.Fatalf("chmod agents dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(agentsDir, 0o700) })

	wf := agentTestWorkflow("do-thing", "agent", "worker")
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) == 0 {
		t.Fatal("expected agent not found since entries cannot be inspected")
	}
	if !strings.Contains(strings.Join(errs, "; "), "not found") {
		t.Errorf("error %q should mention not found", strings.Join(errs, "; "))
	}
}

func TestValidateAgentSkillReferences(t *testing.T) {
	allowed := []string{"allowed"}
	agentRegistry := agents.NewRegistry()
	if err := agentRegistry.Publish(agents.ResolvedAgent{Name: "worker", Skills: &allowed}); err != nil {
		t.Fatal(err)
	}
	skillRegistry := skills.NewRegistry()
	if err := skillRegistry.Register(skills.Definition{Name: "allowed"}); err != nil {
		t.Fatal(err)
	}
	if err := skillRegistry.Register(skills.Definition{Name: "denied"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		agent   string
		skill   string
		wantErr string
	}{
		{name: "legacy unbound"},
		{name: "allowed", skill: "allowed"},
		{name: "unknown agent", agent: "unknown", skill: "allowed", wantErr: `step "do-thing": agent "unknown" not found`},
		{name: "unknown skill", skill: "unknown", wantErr: `step "do-thing": skill "unknown" not found`},
		{name: "not allowed", skill: "denied", wantErr: `step "do-thing": agent "worker" may not use skill "denied"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentName := tc.agent
			if agentName == "" {
				agentName = "worker"
			}
			wf := agentTestWorkflow("do-thing", "agent", agentName)
			wf.Steps[0].Skill = tc.skill
			compiled, err := Compile(wf)
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			errs := ValidateAgentSkillReferences(compiled, agentRegistry, skillRegistry)
			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("ValidateAgentSkillReferences() error = %v", errs)
				}
				return
			}
			if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), tc.wantErr) {
				t.Fatalf("ValidateAgentSkillReferences() error = %v, want %q", errs, tc.wantErr)
			}
		})
	}
}

func TestValidateAgentReferences_AgentPanel(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "review-synthesizer.toml"), []byte("name = \"review-synthesizer\""), 0o644); err != nil {
		t.Fatal(err)
	}
	wf := newAgentPanelWorkflow()
	errs := ValidateAgentReferences(wf, tmpDir)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), `panel member "correctness": agent "panel-reviewer" not found`) {
		t.Fatalf("ValidateAgentReferences() error = %v", errs)
	}
}

func TestValidateAgentSkillReferences_AgentPanel(t *testing.T) {
	allowed := []string{"review-synthesis", "bug-audit"}
	agentRegistry := agents.NewRegistry()
	if err := agentRegistry.Publish(agents.ResolvedAgent{Name: "review-synthesizer", Skills: &allowed}); err != nil {
		t.Fatal(err)
	}
	if err := agentRegistry.Publish(agents.ResolvedAgent{Name: "panel-reviewer", Skills: &allowed}); err != nil {
		t.Fatal(err)
	}
	skillRegistry := skills.NewRegistry()
	if err := skillRegistry.Register(skills.Definition{Name: "review-synthesis"}); err != nil {
		t.Fatal(err)
	}
	if err := skillRegistry.Register(skills.Definition{Name: "bug-audit"}); err != nil {
		t.Fatal(err)
	}

	// agent_panel steps are rejected at Compile (FINDING E6), so exercise the
	// admission function on a compiled workflow built directly, as the CLI does.
	wf := newAgentPanelWorkflow()
	wf.Steps[0].Skill = "review-synthesis"
	wf.Steps[0].Panel.Members[1].Skill = "secure-change"
	compiled := &CompiledWorkflow{Steps: wf.Steps}
	errs := ValidateAgentSkillReferences(compiled, agentRegistry, skillRegistry)
	if len(errs) == 0 || !strings.Contains(strings.Join(errs, "; "), `panel member "security": skill "secure-change" not found`) {
		t.Fatalf("ValidateAgentSkillReferences() error = %v", errs)
	}
}
