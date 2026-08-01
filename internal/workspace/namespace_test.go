package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNamespaceResolvesMivia(t *testing.T) {
	root := "/w"
	if got, want := AgentPromptPath(root), filepath.Join("/w", ".mivia", "agent-prompt.md"); got != want {
		t.Errorf("AgentPromptPath: got %q want %q", got, want)
	}
	if got, want := SkillsDir(root), filepath.Join("/w", ".mivia", "skills"); got != want {
		t.Errorf("SkillsDir: got %q want %q", got, want)
	}
	if got, want := SessionsDir(root), filepath.Join("/w", ".mivia", "sessions"); got != want {
		t.Errorf("SessionsDir: got %q want %q", got, want)
	}
}

func TestNamespaceEmptyRootIsWorkingDirectory(t *testing.T) {
	if got, want := AgentPromptPath(""), filepath.Join(".mivia", "agent-prompt.md"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestUserSkillsDirUsesMiviaHomeNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := UserSkillsDir(), filepath.Join(home, ".mivia", "skills"); got != want {
		t.Errorf("UserSkillsDir: got %q want %q", got, want)
	}
}

// legacyNamespace is the directory mivia used to claim in every user repo.
// It carries no meaning to the binary now: agents read and edit it with the
// ordinary file tools, exactly as they would any other workspace path.
//
// This test is the enforcement for that rule. A fallback, a deprecation
// notice, or a "just one" path constant all reintroduce the squat, and each
// looks harmless in isolation - so the guard is mechanical rather than a
// review convention. See plan 04 (workspace namespace) §3.
// isNestedCheckout reports whether dir is the root of a second checkout of this
// module - a git worktree or a vendored clone - which carries a full copy of
// every file here. A worktree root is marked by a .git entry.
func isNestedCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func TestNoHardcodedLegacyNamespace(t *testing.T) {
	root := repoRoot(t)
	// Hostnames legitimately contain ".ai" (openrouter.ai, api.z.ai), so match
	// the path form only: a quoted ".ai" element or a ".ai/" path prefix.
	legacy := regexp.MustCompile(`"\.ai"|(?:^|[^\w.])\.ai/`)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", "vendor":
				return filepath.SkipDir
			}
			// A git worktree under .claude/worktrees is a second copy of this
			// module: walking in scans every file twice and reports the copy's
			// prefixed path as a fresh offender.
			if path != root && isNestedCheckout(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if legacy.MatchString(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the legacy namespace must not be compiled into the tree; found %d:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file))) // internal/workspace -> repo root
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}
