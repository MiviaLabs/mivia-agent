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
	// The compiled-in default should be concise (< 4KB).
	if len(defaultAgentPrompt) > 3800 {
		t.Fatalf("defaultAgentPrompt is %d bytes, expected < 3800", len(defaultAgentPrompt))
	}
	// Must contain the self-update instruction.
	if !strings.Contains(defaultAgentPrompt, ".mivia/agent-prompt.md") {
		t.Fatal("defaultAgentPrompt must mention .mivia/agent-prompt.md for self-maintenance")
	}
}

func TestDefaultSystemPrompt(t *testing.T) {
	if !strings.Contains(defaultSystemPrompt, "mivia") {
		t.Fatal("defaultSystemPrompt should mention mivia")
	}
}

func TestLoadAgentPromptFallsBack(t *testing.T) {
	// Non-existent directory -> falls back to compiled default.
	prompt := loadAgentPrompt("/tmp/nonexistent-mivia-test-dir-12345")
	if prompt != defaultAgentPrompt {
		t.Fatal("should fall back to defaultAgentPrompt when file missing")
	}
}

func TestLoadAgentPromptFromFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755)
	customPrompt := "custom agent prompt for testing"
	os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte(customPrompt), 0o644)

	prompt := loadAgentPrompt(dir)
	if prompt != customPrompt {
		t.Fatalf("got %q, want %q", prompt, customPrompt)
	}
}

func TestLoadAgentPreferFileOverDefault(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755)
	os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte("override"), 0o644)

	prompt := loadAgentPrompt(dir)
	if prompt == defaultAgentPrompt {
		t.Fatal("should prefer file content over default")
	}
	if prompt != "override" {
		t.Fatalf("got %q, want 'override'", prompt)
	}
}

func TestLoadAgentPromptEmptyFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".mivia"), 0o755)
	os.WriteFile(filepath.Join(dir, ".mivia", "agent-prompt.md"), []byte("   "), 0o644)

	prompt := loadAgentPrompt(dir)
	if prompt != defaultAgentPrompt {
		t.Fatal("empty file should fall back to default")
	}
}

// The legacy namespace carries no meaning: a workspace holding only the old
// paths gets the compiled default, with nothing warning that it was ignored.
// That silence is the accepted cost of compiling in exactly one namespace.
// The clean break of plan 04 §4, asserted as behavior: a workspace holding only
// the legacy paths gets the compiled default prompt and no skills, with nothing
// warning that they were ignored. Named per plan 04 §7; mutation proof M1.
func TestWorkspaceIgnoresLegacyAIDir(t *testing.T) {
	dir := t.TempDir()
	// Legacy layout only — no .mivia/ anywhere.
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

	reg, err := skills.LoadMarkdown(workspace.SkillsDir(dir), nil, "")
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
	// Compiled default must not hardcode this repo's Go toolchain.
	// Project-local verify lives in .mivia/agent-prompt.md when present.
	checks := []string{
		"run_command", "discover", ".mivia/agent-prompt.md", "last resort",
	}
	for _, c := range checks {
		if !strings.Contains(strings.ToLower(defaultAgentPrompt), strings.ToLower(c)) {
			t.Fatalf("defaultAgentPrompt missing generic guidance %q", c)
		}
	}
	if strings.Contains(defaultAgentPrompt, "go test ./...") {
		t.Fatal("defaultAgentPrompt must not hardcode go test ./... (use workspace .mivia/agent-prompt.md)")
	}
}

func TestDefaultAgentPromptMentionsOrchestration(t *testing.T) {
	// The compiled prompt must mention orchestration tools and ADLC.
	checks := []string{
		"spawn_agent", "dispatch_tasks", "delegate",
		"inspect_agents", "join_run", "cancel_run",
		"adlc", "decision tree",
	}
	for _, c := range checks {
		if !strings.Contains(strings.ToLower(defaultAgentPrompt), strings.ToLower(c)) {
			t.Fatalf("defaultAgentPrompt missing orchestration reference %q", c)
		}
	}
}

func TestDefaultAgentPromptNoLanguageBias(t *testing.T) {
	// The compiled prompt must NOT assume a specific language/ecosystem.
	// These are ecosystem-specific filenames that should not appear.
	biased := []string{
		"package.json", "Cargo.toml", "pyproject.toml",
		"Gemfile", "go.mod", "Makefile", "pom.xml",
		"go test", "go build", "go run", "npm test",
	}
	text := strings.ToLower(defaultAgentPrompt)
	for _, b := range biased {
		if strings.Contains(text, strings.ToLower(b)) {
			t.Fatalf("defaultAgentPrompt contains language-biased reference %q", b)
		}
	}
}
