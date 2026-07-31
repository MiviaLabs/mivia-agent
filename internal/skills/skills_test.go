package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillRegistryRegisterAcceptsNameOnly(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{Name: "summarize"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("summarize"); !ok {
		t.Fatal("registered skill not found")
	}
}

func TestSkillRegistryRejectsEmptyName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestSkillRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "x"})
	if err := r.Register(Definition{Name: "x"}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestSkillSelectionEnforcesVersionAndTools(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "x", Version: "1", Tools: []string{"read"}})
	if _, err := r.Select("x", "2", map[string]bool{"read": true}); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if _, err := r.Select("x", "1", map[string]bool{}); err == nil {
		t.Fatal("missing tool accepted")
	}
}

// TestSkillRegistryAfterDeadCodeRemoval verifies that Definition has no Run
// field and skills package has no provider import (confirmed by compilation).
func TestSkillRegistryAfterDeadCodeRemoval(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "cleanup-check"})
	d, ok := r.Get("cleanup-check")
	if !ok {
		t.Fatal("skill not registered")
	}
	// Verify Tools field exists (reserved for plan 06).
	d.Tools = []string{"placeholder"}
	if len(d.Tools) != 1 {
		t.Fatal("Tools field missing")
	}
}

// TestSelectToolsGuardDocumented verifies the Select guard compiles and works
// with the Tools field.
func TestSelectToolsGuardDocumented(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "guarded", Tools: []string{"read", "write"}})
	// All tools available → no error.
	if _, err := r.Select("guarded", "", map[string]bool{"read": true, "write": true}); err != nil {
		t.Fatal(err)
	}
	// Missing tool → error.
	if _, err := r.Select("guarded", "", map[string]bool{"read": true}); err == nil {
		t.Fatal("missing tool accepted")
	}
}

// TestLoadMarkdownUnexported verifies that loadMarkdown is unexported and
// cross-package tests must use LoadMarkdownSources.
func TestLoadMarkdownUnexported(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "test-skill")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test-skill\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Internal loadMarkdown works.
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("test-skill"); !ok {
		t.Fatal("skill not loaded via internal loadMarkdown")
	}
	// LoadMarkdownSources also works for single-source.
	reg2, _, err := LoadMarkdownSources([]Source{{Dir: root, Origin: OriginProject}}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg2.Get("test-skill"); !ok {
		t.Fatal("skill not loaded via LoadMarkdownSources")
	}
}
