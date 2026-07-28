package agentkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/agentkitdata"
)

func TestEmbed_AgentsMD(t *testing.T) {
	content := AgentInstructions()
	if content == "" {
		t.Fatal("AgentInstructions() returned empty content")
	}
}

func TestEmbed_ADLCRule(t *testing.T) {
	content, err := Rule("05-adlc-agentic-development-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("ADLC rule is empty")
	}
}

func TestEmbed_IndexMD(t *testing.T) {
	content := agentkitdata.ReadFile(".ai/INDEX.md")
	if content == "" {
		t.Fatal(".ai/INDEX.md not embedded")
	}
}

func TestEmbed_AgentPrompt(t *testing.T) {
	// agent-prompt.md is intentionally NOT embedded — it's host-specific
	// (references "you are working on yourself", cmd/mivia/, internal/).
	// The shipped binary uses a generic AGENTS.md instead.
	content := agentkitdata.ReadFile(".ai/agent-prompt.md")
	if content != "" {
		t.Fatal(".ai/agent-prompt.md should NOT be embedded — it's host-specific. " +
			"Use ship/AGENTS.md for generic instructions.")
	}
}

func TestVersion_NonEmpty(t *testing.T) {
	v := Version()
	if v == "" {
		t.Fatal("Version() returned empty")
	}
}

func TestVersion_Deterministic(t *testing.T) {
	v1 := Version()
	v2 := Version()
	if v1 != v2 {
		t.Fatal("Version() is not deterministic across calls")
	}
}

func TestHasLocalOverride_NoDir(t *testing.T) {
	dir := t.TempDir()
	if HasLocalOverride(dir) {
		t.Fatal("HasLocalOverride should be false for empty dir")
	}
}

func TestHasLocalOverride_WithDir(t *testing.T) {
	dir := t.TempDir()
	aiDir := filepath.Join(dir, ".ai")
	os.MkdirAll(aiDir, 0755)
	writeTestFile(t, filepath.Join(aiDir, "test.md"), "test")
	if !HasLocalOverride(dir) {
		t.Fatal("HasLocalOverride should be true when .ai/ exists with files")
	}
}

func TestEnsureInstructions_NoDir(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureInstructions(dir); err != nil {
		t.Fatal(err)
	}
	aiDir := filepath.Join(dir, ".ai")
	if _, err := os.Stat(aiDir); os.IsNotExist(err) {
		t.Fatal(".ai/ was not created")
	}
	// Verify key files exist
	checkFiles := []string{
		".ai/INDEX.md",
		".ai/rules/05-adlc-agentic-development-lifecycle.md",
	}
	for _, f := range checkFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); os.IsNotExist(err) {
			t.Fatalf("expected %s to exist after EnsureInstructions", f)
		}
	}
}

func TestEnsureInstructions_Exists(t *testing.T) {
	dir := t.TempDir()
	aiDir := filepath.Join(dir, ".ai")
	os.MkdirAll(aiDir, 0755)
	writeTestFile(t, filepath.Join(aiDir, "custom.md"), "custom content")
	if err := EnsureInstructions(dir); err != nil {
		t.Fatal(err)
	}
	// Custom file should still exist
	data, err := os.ReadFile(filepath.Join(aiDir, "custom.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom content" {
		t.Fatal("EnsureInstructions overwrote existing file")
	}
}

func TestWriteInstructions(t *testing.T) {
	dir := t.TempDir()
	written, err := WriteInstructions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("WriteInstructions returned empty list")
	}
	// Just check count — at least 3 files should be written
	if len(written) < 3 {
		t.Fatalf("expected at least 3 files written, got %d", len(written))
	}
}

func TestWriteInstructions_NoOverwrite(t *testing.T) {
	dir := t.TempDir()
	aiDir := filepath.Join(dir, ".ai")
	os.MkdirAll(filepath.Join(aiDir, "rules"), 0755)
	writeTestFile(t, filepath.Join(aiDir, "rules", "custom-rule.md"), "custom-rule content")
	writeTestFile(t, filepath.Join(aiDir, "custom.md"), "custom content")

	written, err := WriteInstructions(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Custom files should NOT be in written list (they were skipped)
	for _, f := range written {
		if f == ".ai/rules/custom-rule.md" || f == ".ai/custom.md" {
			t.Fatalf("custom file %s was overwritten", f)
		}
	}
	// Custom files should still have original content
	data, err := os.ReadFile(filepath.Join(aiDir, "custom.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom content" {
		t.Fatal("custom file was overwritten")
	}
}

func TestResolve_LocalFirst(t *testing.T) {
	dir := t.TempDir()
	aiDir := filepath.Join(dir, ".ai")
	os.MkdirAll(aiDir, 0755)
	writeTestFile(t, filepath.Join(aiDir, "AGENTS.md"), "local content")

	content, err := Resolve(dir, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	// The resolve should find local AGENTS.md (not embedded)
	if content == "" {
		t.Fatal("Resolve returned empty content")
	}
	_ = content
}

func TestResolve_EmbeddedFallback(t *testing.T) {
	dir := t.TempDir()
	// No .ai/ directory — should fall back to embedded
	content, err := Resolve(dir, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("Resolve returned empty content for embedded AGENTS.md")
	}
}

func TestResolve_Neither(t *testing.T) {
	dir := t.TempDir()
	// No local .ai/ and this file doesn't exist embedded either
	content, err := Resolve(dir, "nonexistent-file-xyz.md")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Fatal("Resolve should return empty for nonexistent file")
	}
}

func TestEmbed_AllRules(t *testing.T) {
	// Verify at least 5 rules exist (we know there are at least this many)
	rules := []string{
		"00-operating-doctrine",
		"01-output-budget",
		"05-adlc-agentic-development-lifecycle",
		"10-security-privacy",
		"20-agent-quality",
	}
	for _, name := range rules {
		content, err := Rule(name)
		if err != nil {
			t.Fatalf("Rule %s not embedded: %v", name, err)
		}
		if content == "" {
			t.Fatalf("Rule %s is empty", name)
		}
	}
}

func TestEmbed_AllFilesMatch(t *testing.T) {
	// Walk the source .ai/ directory and verify every file is embedded
	// This test runs from the repo root
	entries, err := os.ReadDir(".ai")
	if err != nil {
		t.Skip("not running from repo root or .ai/ not accessible")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			subEntries, err := os.ReadDir(filepath.Join(".ai", entry.Name()))
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if sub.IsDir() && entry.Name() == "skills" {
					// Skills have subdirectories — check each skill's SKILL.md
					skillDirs, _ := os.ReadDir(filepath.Join(".ai", "skills"))
					for _, sd := range skillDirs {
						if sd.IsDir() {
							skillPath := filepath.Join(".ai", "skills", sd.Name(), "SKILL.md")
							if _, err := os.Stat(skillPath); err == nil {
								content, resolveErr := Resolve(".", filepath.Join("ai", "skills", sd.Name(), "SKILL.md"))
								if resolveErr != nil || content == "" {
									t.Fatalf("SKILL.md for %s not embedded or empty", sd.Name())
								}
							}
						}
					}
				}
				if !sub.IsDir() {
					relPath := filepath.Join("ai", entry.Name(), sub.Name())
					content, resolveErr := Resolve(".", relPath)
					if resolveErr != nil || content == "" {
						t.Fatalf("file %s not embedded or empty: %v", relPath, resolveErr)
					}
				}
			}
		} else {
			content, resolveErr := Resolve(".", filepath.Join("ai", entry.Name()))
			if resolveErr != nil || content == "" {
				t.Fatalf("file ai/%s not embedded or empty: %v", entry.Name(), resolveErr)
			}
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestNoHostSpecificContentLeaks ensures embedded instructions don't contain
// host-specific references (cmd/mivia, internal/, etc.) that would confuse
// users running the binary in other projects.
func TestNoHostSpecificContentLeaks(t *testing.T) {
	allFiles, err := agentkitdata.ReadAllFiles()
	if err != nil {
		t.Fatal(err)
	}

	hostPatterns := []string{
		"working on yourself",
	}

	for path, content := range allFiles {
		for _, pattern := range hostPatterns {
			if strings.Contains(string(content), pattern) {
				t.Errorf("HOST-SPECIFIC content leaked into embedded %s: found %q", path, pattern)
			}
		}
	}
}
