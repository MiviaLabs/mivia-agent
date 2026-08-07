package template

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

func TestLoadTemplates_DoubleDotInName(t *testing.T) {
	// A directory name containing ".." as a literal substring is safe.
	dir := filepath.Join(t.TempDir(), "v1..2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("expected no error for directory with .. in name, got: %v", err)
	}
	if m["plan.md"] != "# plan" {
		t.Errorf("expected plan.md content, got %v", m)
	}

	// Real traversal still fails.
	if _, err := LoadTemplates("../etc/passwd"); err == nil {
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

// --- LoadTemplates error branches ---

func TestLoadTemplates_BaseDirIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadTemplates(file)
	if err == nil {
		t.Fatal("expected error for file-based template dir")
	}
	if !strings.Contains(err.Error(), "not a real directory") {
		t.Errorf("error %q should mention not a real directory", err.Error())
	}
}

func TestLoadTemplates_SkipsNonMdFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 || m["plan.md"] == "" {
		t.Errorf("expected only plan.md, got %v", m)
	}
}

func TestLoadTemplates_SymlinkDirRejected(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := LoadTemplates(link)
	if err == nil {
		t.Fatal("expected error for symlinked template dir")
	}
	if !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Errorf("error %q should mention symbolic link", err.Error())
	}
}

func TestLoadTemplates_MissingBaseDir(t *testing.T) {
	m, err := LoadTemplates(filepath.Join(t.TempDir(), "notemplates"))
	if err != nil {
		t.Fatalf("expected nil error for missing template dir, got: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil map, got %v", m)
	}
}

func TestLoadTemplates_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	dir := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(dir, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := LoadTemplates(dir); err == nil {
		t.Fatal("expected error for unreadable template dir")
	}
}

func TestLoadTemplates_NonSearchableDir(t *testing.T) {
	// A read-only (0o400) template dir opens as a root but its entries cannot
	// be listed, so loading surfaces the ReadDir error.
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	dir := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(dir, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := LoadTemplates(dir); err == nil {
		t.Fatal("expected an error for a non-listable template directory")
	}
}

// --- readTemplateFile error branches ---

// TestReadTemplateFileMissingFile covers the Lstat error branch of
// readTemplateFile directly: a name that vanished between discovery's
// ReadDir and the per-file read.
func TestReadTemplateFileMissingFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readTemplateFile(root, "missing.md"); err == nil {
		t.Fatal("expected an error for a missing template file")
	}
}

func TestLoadTemplates_SymlinkFileRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("# real"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "real.md"), filepath.Join(dir, "a-link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := LoadTemplates(dir)
	if err == nil {
		t.Fatal("expected error for symlinked template file")
	}
	if !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Errorf("error %q should mention symbolic link", err.Error())
	}
}

func TestLoadTemplates_NonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "a-pipe.md"), 0o644); err != nil {
		t.Skipf("mkfifo not supported: %v", err)
	}
	_, err := LoadTemplates(dir)
	if err == nil {
		t.Fatal("expected error for non-regular template file")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("error %q should mention not a regular file", err.Error())
	}
}

func TestLoadTemplates_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a.md")
	if err := os.WriteFile(file, []byte("# a"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
	if _, err := LoadTemplates(dir); err == nil {
		t.Fatal("expected error for unreadable template file")
	}
}

func TestLoadTemplates_OversizedFile(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", MaxTemplateBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadTemplates(dir)
	if err == nil {
		t.Fatal("expected error for oversized template file")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q should mention exceeds", err.Error())
	}
}
