package template

import (
	"path/filepath"
	"runtime"
	"testing"
)

// templatesDir returns the path to the shared testdata/templates directory.
func templatesDir(t *testing.T) string {
	t.Helper()
	// Use runtime.Caller to resolve relative to this test file's location.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "testdata", "templates")
}

func TestLoadTemplates_NonexistentDir(t *testing.T) {
	m, err := LoadTemplates("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil map, got: %v", m)
	}
}

func TestLoadTemplates_EmptyDir(t *testing.T) {
	_, err := LoadTemplates("")
	if err == nil {
		t.Fatal("expected error for empty dir, got nil")
	}
}

func TestLoadTemplates_WhitespaceDir(t *testing.T) {
	_, err := LoadTemplates("   ")
	if err == nil {
		t.Fatal("expected error for whitespace dir, got nil")
	}
}

func TestLoadTemplates_PathTraversal(t *testing.T) {
	// A path starting with ".." remains after filepath.Clean.
	_, err := LoadTemplates("../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestLoadTemplates_ReadsMdFiles(t *testing.T) {
	td := templatesDir(t)
	m, err := LoadTemplates(td)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil map")
	}

	expected := []string{"plan.md", "implement.md", "review.md"}
	for _, name := range expected {
		content, ok := m[name]
		if !ok {
			t.Errorf("expected template %q to be loaded", name)
			continue
		}
		if len(content) == 0 {
			t.Errorf("template %q has empty content", name)
		}
	}
}

func TestLoadTemplates_SkipsNonMdFiles(t *testing.T) {
	td := templatesDir(t)
	m, err := LoadTemplates(td)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only .md files should be present
	for name := range m {
		if len(name) < 3 || name[len(name)-3:] != ".md" {
			t.Errorf("expected only .md files, found %q", name)
		}
	}
}

func TestValidateReferences_AllPresent(t *testing.T) {
	td := templatesDir(t)
	m, err := LoadTemplates(td)
	if err != nil {
		t.Fatalf("unexpected error loading templates: %v", err)
	}

	missing := ValidateReferences(m, []string{"plan.md", "implement.md", "review.md"})
	if len(missing) != 0 {
		t.Errorf("expected no missing templates, got: %v", missing)
	}
}

func TestValidateReferences_MissingTemplates(t *testing.T) {
	td := templatesDir(t)
	m, err := LoadTemplates(td)
	if err != nil {
		t.Fatalf("unexpected error loading templates: %v", err)
	}

	missing := ValidateReferences(m, []string{"plan.md", "nonexistent.md"})
	if len(missing) != 1 || missing[0] != "nonexistent.md" {
		t.Errorf("expected [nonexistent.md], got: %v", missing)
	}
}

func TestValidateReferences_SkipsEmpty(t *testing.T) {
	td := templatesDir(t)
	m, err := LoadTemplates(td)
	if err != nil {
		t.Fatalf("unexpected error loading templates: %v", err)
	}

	missing := ValidateReferences(m, []string{"plan.md", "", "  ", "review.md"})
	if len(missing) != 0 {
		t.Errorf("expected no missing templates (empty strings skipped), got: %v", missing)
	}
}

func TestValidateReferences_EmptyMap(t *testing.T) {
	missing := ValidateReferences(map[string]string{}, []string{"anything.md"})
	if len(missing) != 1 || missing[0] != "anything.md" {
		t.Errorf("expected [anything.md], got: %v", missing)
	}
}
