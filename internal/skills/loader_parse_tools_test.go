package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSingleParseSkillMarkdown verifies that parseSkillMarkdown extracts all
// known keys from a single parse call and produces correct instructions.
func TestSingleParseSkillMarkdown(t *testing.T) {
	input := []byte("---\nname: review\ndescription: Review code\ntriggers:\n  - arch review\n  - design review\nargument-hint: <path>\nshort-description: Quick review\nuser-invocable: false\ntools:\n  - read_file\n  - grep\n---\nReview instructions here.\n")
	parsed, err := parseSkillMarkdown(input)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.name != "review" {
		t.Fatalf("name=%q", parsed.name)
	}
	if parsed.description != "Review code" {
		t.Fatalf("description=%q", parsed.description)
	}
	if len(parsed.triggers) != 2 || parsed.triggers[0] != "arch review" || parsed.triggers[1] != "design review" {
		t.Fatalf("triggers=%v", parsed.triggers)
	}
	if parsed.argsHint != "<path>" {
		t.Fatalf("argsHint=%q", parsed.argsHint)
	}
	if parsed.shortDescription != "Quick review" {
		t.Fatalf("shortDescription=%q", parsed.shortDescription)
	}
	if parsed.userInvocable {
		t.Fatal("userInvocable should be false")
	}
	if len(parsed.tools) != 2 || parsed.tools[0] != "read_file" || parsed.tools[1] != "grep" {
		t.Fatalf("tools=%v", parsed.tools)
	}
	if !strings.Contains(parsed.instructions, "Review instructions here") {
		t.Fatalf("instructions=%q", parsed.instructions)
	}
}

// TestSkillToolsParsedAndPublished proves frontmatter tools reach Definition.Tools
// with non-empty values when the fixture declares them (plan 06 phase 01).
func TestSkillToolsParsedAndPublished(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "audit")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: audit\ndescription: Audit\ntools:\n  - read_file\n  - grep\n  - run_command\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("audit")
	if !ok {
		t.Fatal("skill missing")
	}
	if len(def.Tools) == 0 {
		t.Fatal("Definition.Tools must be non-empty when fixture declares tools")
	}
	want := []string{"read_file", "grep", "run_command"}
	if len(def.Tools) != len(want) {
		t.Fatalf("Tools=%v want %v", def.Tools, want)
	}
	for i, n := range want {
		if def.Tools[i] != n {
			t.Fatalf("Tools[%d]=%q want %q", i, def.Tools[i], n)
		}
	}
	if def.Origin != OriginProject {
		t.Fatalf("origin=%q", def.Origin)
	}
}

func TestSkillToolsFlowSequence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: [read_file, write_file]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if len(def.Tools) != 2 || def.Tools[0] != "read_file" || def.Tools[1] != "write_file" {
		t.Fatalf("Tools=%v", def.Tools)
	}
}

func TestSkillToolsScalarAcceptedAsSingleton(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: read_file\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if len(def.Tools) != 1 || def.Tools[0] != "read_file" {
		t.Fatalf("Tools=%v", def.Tools)
	}
}

func TestSkillToolsMalformedRejected(t *testing.T) {
	// Empty list item is rejected by the frontmatter parser.
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools:\n  - \n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("malformed tools list must be rejected")
	}
}

func TestSkillToolsEmptyNameRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Quoted empty string survives frontmatter as "" - parser must reject it.
	content := "---\nname: x\ntools: [\"\"]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMarkdown(root); err == nil {
		t.Fatal("empty tool name must be rejected")
	}
}

func TestSkillToolsOmittedIsNil(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if def.Tools != nil {
		t.Fatalf("omitted tools must stay nil, got %v", def.Tools)
	}
}

func TestSkillToolsEmptyListIsNonNilEmpty(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\ntools: []\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, _ := reg.Get("x")
	if def.Tools == nil {
		t.Fatal("explicit tools: [] must be non-nil empty, not omitted nil")
	}
	if len(def.Tools) != 0 {
		t.Fatalf("Tools=%v", def.Tools)
	}
}
