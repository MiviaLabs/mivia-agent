package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Plan 43: duplicate tool names within one tools: list are rejected, not
// silently deduplicated.
func TestSkillToolsDuplicateNamesRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: [read_file, read_file]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("duplicate tools entries must be rejected")
	}
	// The resilient multi-source loader skips the offending skill with a
	// bounded warning instead of failing chat startup.
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(reg.List()) != 0 || len(warnings) != 1 {
		t.Fatalf("resilient registry=%v warnings=%v err=%v", reg, warnings, err)
	}
}

// Plan 43: a skill that statically declares an unknown tool name is rejected
// by the strict loader and skipped with a bounded warning by the resilient
// loader. The warning must not echo the skill or tool name.
func TestSkillToolsUnknownNameRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: [read_file, not_a_tool]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("unknown static tool must be rejected by the strict loader")
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(reg.List()) != 0 || len(warnings) != 1 {
		t.Fatalf("resilient registry=%v warnings=%v err=%v", reg, warnings, err)
	}
	if strings.Contains(strings.Join(warnings, "\n"), "not_a_tool") {
		t.Fatalf("warning leaked tool name: %v", warnings)
	}
}

// Plan 43: a skill that statically declares the activation-only
// read_skill_resource capability is rejected like any unknown static tool.
func TestSkillToolsCannotDeclareActivationOnlyCapability(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: [read_file, read_skill_resource]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("read_skill_resource must not be a statically declared skill tool")
	}
	reg, warnings, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil || len(reg.List()) != 0 || len(warnings) != 1 {
		t.Fatalf("resilient registry=%v warnings=%v err=%v", reg, warnings, err)
	}
}
