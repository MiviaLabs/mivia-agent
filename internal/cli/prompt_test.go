package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAgentPromptIsShort(t *testing.T) {
	// The compiled-in default should be concise (< 4KB).
	if len(defaultAgentPrompt) > 3800 {
		t.Fatalf("defaultAgentPrompt is %d bytes, expected < 3800", len(defaultAgentPrompt))
	}
	// Must contain the self-update instruction.
	if !strings.Contains(defaultAgentPrompt, ".ai/agent-prompt.md") {
		t.Fatal("defaultAgentPrompt must mention .ai/agent-prompt.md for self-maintenance")
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
	os.MkdirAll(filepath.Join(dir, ".ai"), 0o755)
	customPrompt := "custom agent prompt for testing"
	os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte(customPrompt), 0o644)

	prompt := loadAgentPrompt(dir)
	if prompt != customPrompt {
		t.Fatalf("got %q, want %q", prompt, customPrompt)
	}
}

func TestLoadAgentPreferFileOverDefault(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".ai"), 0o755)
	os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte("override"), 0o644)

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
	os.MkdirAll(filepath.Join(dir, ".ai"), 0o755)
	os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte("   "), 0o644)

	prompt := loadAgentPrompt(dir)
	if prompt != defaultAgentPrompt {
		t.Fatal("empty file should fall back to default")
	}
}

func TestEnsureAgentPromptFileCreates(t *testing.T) {
	dir := t.TempDir()
	path, created, err := ensureAgentPromptFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected new file to be created")
	}
	if !strings.HasSuffix(path, ".ai/agent-prompt.md") && !strings.HasSuffix(path, ".ai\\agent-prompt.md") {
		t.Fatalf("unexpected path: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != defaultAgentPrompt+"\n" {
		t.Fatalf("content mismatch:\ngot:  %q\nwant: %q", string(data), defaultAgentPrompt+"\n")
	}
}

func TestEnsureAgentPromptFileExisting(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".ai"), 0o755)
	os.WriteFile(filepath.Join(dir, ".ai", "agent-prompt.md"), []byte("existing"), 0o644)

	path, created, err := ensureAgentPromptFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("should not create new file when one exists")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Fatalf("should not overwrite existing file, got %q", string(data))
	}
}

func TestAgentPromptPathConstant(t *testing.T) {
	if agentPromptPath != ".ai/agent-prompt.md" {
		t.Fatalf("agentPromptPath=%q", agentPromptPath)
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
	// Project-local verify lives in .ai/agent-prompt.md when present.
	checks := []string{
		"run_command", "discover", ".ai/agent-prompt.md", "last resort",
	}
	for _, c := range checks {
		if !strings.Contains(strings.ToLower(defaultAgentPrompt), strings.ToLower(c)) {
			t.Fatalf("defaultAgentPrompt missing generic guidance %q", c)
		}
	}
	if strings.Contains(defaultAgentPrompt, "go test ./...") {
		t.Fatal("defaultAgentPrompt must not hardcode go test ./... (use workspace .ai/agent-prompt.md)")
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
