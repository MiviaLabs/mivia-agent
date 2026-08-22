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
	if got, want := SkillsDir(root), filepath.Join("/w", ".mivia", "skills"); got != want {
		t.Errorf("SkillsDir: got %q want %q", got, want)
	}
	if got, want := SessionsDir(root), filepath.Join("/w", ".mivia", "sessions"); got != want {
		t.Errorf("SessionsDir: got %q want %q", got, want)
	}
}

func TestWorktreesDir(t *testing.T) {
	root := "/w"
	if got, want := WorktreesDir(root), filepath.Join("/w", ".mivia", "worktrees"); got != want {
		t.Errorf("WorktreesDir: got %q want %q", got, want)
	}
}

func TestNamespaceEmptyRootIsWorkingDirectory(t *testing.T) {
	if got, want := SkillsDir(""), filepath.Join(".mivia", "skills"); got != want {
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

func TestUserHomeDirUsesHomeOnAllPlatforms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got != home {
		t.Fatalf("UserHomeDir = %q, want %q", got, home)
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

// namespaceAllowlist names the production Go lines OUTSIDE internal/workspace
// that must keep naming the namespace directory in user-facing text. Keyed by
// (relative path, trimmed line) so a move or reword fails loudly instead of
// silently re-blessing a stale entry; the value is the reason the literal is
// allowed. Every other production occurrence of the namespace name is a
// defect: the name is single-sourced in Namespace, and a second convention
// grows back one call site at a time. This is the mechanical mirror of
// TestNoHardcodedLegacyNamespace for the CURRENT name (INV-AG-37).
var namespaceAllowlist = map[string]string{
	"internal/clichat/chat_slash_handlers.go\x00sink.Info(\"no agents loaded (add .mivia/agents/<name>.toml)\")":                                                                                            "user-facing /agent log line that tells the operator where to add agent definitions",
	"internal/cli/hooks_command.go\x00const hookProjectNotice = \"hooks marked [project] came from this workspace's .mivia/mivia.toml, not from your \" +":                                                  "user-facing /hooks notice naming the workspace hook config file",
	"internal/cli/hooks_command.go\x00\"workspace's .mivia/mivia.toml runs. Delete or comment out the [[hooks]] entry to stop one.\"":                                                                       "user-facing /hooks trust explanation naming the workspace hook config file",
	"internal/cli/hooks_command.go\x00\"a hook declared in ~/.mivia/mivia.toml runs without confirmation.\")":                                                                                               "user-facing --bypass-hook-trust notice naming the user hook config file",
	"internal/cli/hooks_command.go\x00return \"no lifecycle hooks configured (they load from ~/.mivia/mivia.toml and <workspace>/.mivia/mivia.toml)\"":                                                      "user-facing /hooks empty listing naming both hook config surfaces",
	"internal/clichat/prompt.go\x00Project agents (if present): .mivia/agents/<name>.toml - default root agent is \"mivia\".":                                                                               "defaultAgentPrompt self-maintenance text (pinned by TestPromptSelfMaintenance): the model must know where project agents live",
	"internal/cli/root.go\x00--agent selects a named agent definition from ~/.mivia/agents/ or <workspace>/.mivia/agents/.":                                                                                 "user-facing CLI help for --agent",
	"internal/cli/root.go\x00Config: $MIVIA_CONFIG | ./.mivia/mivia.toml | ~/.mivia/mivia.toml":                                                                                                             "user-facing CLI help listing the config search path",
	"internal/config/load.go\x00return File{}, \"\", false, fmt.Errorf(\"no config file found (tried %s); set MIVIA_CONFIG or create .mivia/mivia.toml\", strings.Join(DefaultConfigCandidates(), \", \"))": "user-facing config error that tells the operator what to create",
}

// TestNamespaceNameSingleSourced is the mechanical enforcement of the
// namespace.go contract "Nothing outside this file may name a namespace
// directory" for the CURRENT namespace name (the legacy .ai name has its own
// guard, TestNoHardcodedLegacyNamespace). Production code must route through
// Namespace/NamespacePath; without the guard a rename of Namespace silently
// diverges surfaces: config read from the old name while agents/skills load
// from the new, orphaned worktree markers, sandbox no longer excluding the
// control directory, delivery policy silently unapplied.
func TestNamespaceNameSingleSourced(t *testing.T) {
	root := repoRoot(t)
	re := regexp.MustCompile(`"\.mivia"|(?:^|[^\w.])\.mivia/`)
	workspacePkg := filepath.Join(root, "internal", "workspace")

	var offenders []string
	used := make(map[string]bool)
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
			// prefixed path as a fresh offender. The workspace package itself
			// is where the name is allowed to live.
			if path != root && (isNestedCheckout(path) || path == workspacePkg) {
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
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if !re.MatchString(line) {
				continue
			}
			key := filepath.ToSlash(rel) + "\x00" + trimmed
			if _, ok := namespaceAllowlist[key]; ok {
				used[key] = true
				continue
			}
			offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+trimmed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the namespace directory must be named only in internal/workspace (see namespace.go); found %d production occurrences:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
	for key := range namespaceAllowlist {
		if !used[key] {
			t.Errorf("stale namespace allowlist entry (no matching line in the tree): %q", key)
		}
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
