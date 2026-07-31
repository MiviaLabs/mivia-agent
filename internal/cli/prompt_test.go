package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestDefaultAgentPromptIsShort(t *testing.T) {
	if len(defaultAgentPrompt) > 3800 {
		t.Fatalf("defaultAgentPrompt is %d bytes, expected < 3800", len(defaultAgentPrompt))
	}
	if !strings.Contains(defaultAgentPrompt, ".mivia/agents/") {
		t.Fatal("defaultAgentPrompt must mention .mivia/agents/ for self-maintenance")
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystemPrompt, "mivia") {
		t.Fatal("defaultSystemPrompt should mention mivia")
	}
}

func TestLoadAgentPromptFallsBack(t *testing.T) {
	// Compiled fallback only — agent-prompt.md is not read.
	prompt := loadAgentPrompt("/tmp/nonexistent-mivia-test-dir-12345")
	if prompt != defaultAgentPrompt {
		t.Fatal("should fall back to defaultAgentPrompt")
	}
}

func TestLoadAgentPromptIgnoresAgentPromptMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte("should not load"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := loadAgentPrompt(dir)
	if prompt == "should not load" {
		t.Fatal("agent-prompt.md must not be loaded; use .mivia/agents/*.toml")
	}
	if prompt != defaultAgentPrompt {
		t.Fatalf("got %q, want compiled default", prompt)
	}
}

// The legacy namespace carries no meaning: a workspace holding only the old
// paths gets the compiled default, with nothing warning that it was ignored.
func TestWorkspaceIgnoresLegacyAIDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ai", "skills", "legacy-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte("legacy prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillBody := "---\nname: legacy-skill\ndescription: should not load\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, ".ai", "skills", "legacy-skill", "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := loadAgentPrompt(dir); got != defaultAgentPrompt {
		t.Errorf("legacy .ai/agent-prompt.md must be ignored, got %q", got)
	}

	reg, _, err := skills.LoadMarkdownSources([]skills.Source{{Dir: workspace.SkillsDir(dir), Origin: skills.OriginProject}}, skills.LoadOptions{})
	if err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if n := len(reg.List()); n != 0 {
		t.Errorf("legacy .ai/skills must not load, got %d skills", n)
	}
}

func TestLoadAgentPromptEmptyDir(t *testing.T) {
	prompt := loadAgentPrompt("")
	if prompt != defaultAgentPrompt {
		t.Fatal("empty workspaceDir should fall back to default")
	}
}

func TestDefaultAgentPromptHasGenericVerifyGuidance(t *testing.T) {
	lower := strings.ToLower(defaultAgentPrompt)
	checks := []string{
		"run_command", "discover", ".mivia/agents/", "last resort",
	}
	for _, c := range checks {
		if !strings.Contains(lower, strings.ToLower(c)) {
			t.Errorf("defaultAgentPrompt missing %q", c)
		}
	}
	if strings.Contains(defaultAgentPrompt, "go test ./...") {
		t.Fatal("defaultAgentPrompt must not hardcode go test ./... (use .mivia/agents/*.toml)")
	}
}
