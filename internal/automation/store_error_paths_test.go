package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestAutomationsFilePathRejectsScopeBuiltin covers automationsFilePath's
// default branch (store.go): ScopeBuiltin has no automations.toml behind
// it (ports.Scope's own doc: "Builtin rows are read-only: there is no
// file behind them"), so both LoadSpecs and SaveSpecs must refuse it
// with a named error rather than silently resolving an empty path.
func TestAutomationsFilePathRejectsScopeBuiltin(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadSpecs(ports.ScopeBuiltin, root); err == nil {
		t.Fatal("LoadSpecs(ScopeBuiltin): got nil error, want a rejection")
	} else if !strings.Contains(err.Error(), "has no automations.toml") {
		t.Fatalf("LoadSpecs(ScopeBuiltin) error = %q, want it naming the unsupported scope", err.Error())
	}
	if err := SaveSpecs(ports.ScopeBuiltin, root, []Spec{sampleSpec("x")}); err == nil {
		t.Fatal("SaveSpecs(ScopeBuiltin): got nil error, want a rejection")
	}
}

// TestAutomationsFilePathRejectsEmptyProjectRoot covers automationsFilePath's
// ports.ScopeProject branch's own guard: an empty workspaceRoot has no
// project-scoped .mivia directory to resolve against.
func TestAutomationsFilePathRejectsEmptyProjectRoot(t *testing.T) {
	if _, err := LoadSpecs(ports.ScopeProject, ""); err == nil {
		t.Fatal("LoadSpecs(ScopeProject, \"\"): got nil error, want a rejection")
	} else if !strings.Contains(err.Error(), "requires a workspace root") {
		t.Fatalf("error = %q, want it naming the missing workspace root", err.Error())
	}
}

// TestAutomationsFilePathRejectsEmptyHome covers automationsFilePath's
// ports.ScopeUser branch's error wrap around workspace.UserHomeDir: HOME
// set-but-empty is UserHomeDir's own documented failure
// (internal/workspace/namespace.go:31), reached deterministically by
// setting the env var rather than unsetting it (unsetting falls through
// to os.UserHomeDir, which succeeds in any normal test environment).
func TestAutomationsFilePathRejectsEmptyHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := LoadSpecs(ports.ScopeUser, ""); err == nil {
		t.Fatal("LoadSpecs(ScopeUser) with empty HOME: got nil error, want a rejection")
	} else if !strings.Contains(err.Error(), "resolve user home") {
		t.Fatalf("error = %q, want it naming the user-home resolution failure", err.Error())
	}
}

// TestLoadSpecsRejectsUnreadableFile covers LoadSpecs' os.ReadFile error
// branch for a failure OTHER than not-exist: a directory in place of the
// expected file makes ReadFile fail with a real, portable I/O error
// (EISDIR) rather than ErrNotExist, so the os.IsNotExist(err) branch is
// not taken and LoadSpecs must return the read-error wrap instead of a
// silent empty slice.
func TestLoadSpecsRejectsUnreadableFile(t *testing.T) {
	root := t.TempDir()
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir a directory at the expected file path: %v", err)
	}
	if _, err := LoadSpecs(ports.ScopeProject, root); err == nil {
		t.Fatal("LoadSpecs with a directory at the file path: got nil error, want a read failure")
	} else if !strings.Contains(err.Error(), "read") {
		t.Fatalf("error = %q, want it naming the read failure", err.Error())
	}
}

// TestLoadSpecsRejectsMalformedTOML covers LoadSpecs' toml.Unmarshal
// error branch: a syntactically invalid automations.toml must fail the
// whole load with a named parse error, not silently produce an empty
// list.
func TestLoadSpecsRejectsMalformedTOML(t *testing.T) {
	root := t.TempDir()
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not valid toml {{{"), 0o600); err != nil {
		t.Fatalf("write malformed toml: %v", err)
	}
	if _, err := LoadSpecs(ports.ScopeProject, root); err == nil {
		t.Fatal("LoadSpecs with malformed TOML: got nil error, want a parse failure")
	} else if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error = %q, want it naming the parse failure", err.Error())
	}
}

// TestLoadSpecsUsesTableKeyWhenSpecIDIsEmpty covers LoadSpecs' fallback
// (store.go: "if spec.ID == "" { spec.ID = id }"): a hand-edited
// automations.toml whose [automations.<id>] table key carries the id but
// whose id="" field was accidentally left blank must still resolve to
// the table key rather than fail id validation.
func TestLoadSpecsUsesTableKeyWhenSpecIDIsEmpty(t *testing.T) {
	root := t.TempDir()
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := "[automations.from-table-key]\nname = \"x\"\nenabled = true\n[[automations.from-table-key.steps]]\nkind = \"prompt\"\nprompt = \"hi\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	got, err := LoadSpecs(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("LoadSpecs: %v", err)
	}
	if len(got) != 1 || got[0].ID != "from-table-key" {
		t.Fatalf("LoadSpecs = %#v, want one spec with ID resolved from the table key", got)
	}
}

// TestLoadSpecsRejectsInvalidSpecFromDisk covers LoadSpecs' per-entry
// ValidateSpec error branch: a hand-edited file with a structurally
// invalid automation (here, empty steps) must fail the whole load with a
// wrapped, path-qualified error - not silently drop the bad entry.
func TestLoadSpecsRejectsInvalidSpecFromDisk(t *testing.T) {
	root := t.TempDir()
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	doc := "[automations.bad]\nname = \"x\"\nenabled = true\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	if _, err := LoadSpecs(ports.ScopeProject, root); err == nil {
		t.Fatal("LoadSpecs with an invalid on-disk spec: got nil error, want rejection")
	} else if !strings.Contains(err.Error(), "steps must be non-empty") {
		t.Fatalf("error = %q, want it naming the validation failure", err.Error())
	}
}

// TestSaveSpecsRejectsUnwritableDir covers SaveSpecs' os.MkdirAll error
// branch: pre-creating a REGULAR FILE at the target directory path makes
// MkdirAll fail (it cannot descend through a non-directory), a portable,
// deterministic trigger.
func TestSaveSpecsRejectsUnwritableDir(t *testing.T) {
	root := t.TempDir()
	blockerPath, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	blockerDir := filepath.Dir(blockerPath)
	if err := os.MkdirAll(filepath.Dir(blockerDir), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// A plain file sitting where the .mivia directory must go: MkdirAll
	// cannot create a directory through an existing non-directory path
	// component.
	if err := os.WriteFile(blockerDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{sampleSpec("x")}); err == nil {
		t.Fatal("SaveSpecs with a file blocking the target directory: got nil error, want rejection")
	} else if !strings.Contains(err.Error(), "create dir") {
		t.Fatalf("error = %q, want it naming the mkdir failure", err.Error())
	}
}

// TestWriteFileAtomicRejectsUnwritableDir covers writeFileAtomic's
// os.CreateTemp error branch directly: a nonexistent directory makes
// CreateTemp fail deterministically and portably.
func TestWriteFileAtomicRejectsUnwritableDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := writeFileAtomic(missing, "automations.toml", []byte("x")); err == nil {
		t.Fatal("writeFileAtomic into a nonexistent directory: got nil error, want rejection")
	} else if !strings.Contains(err.Error(), "create temp file") {
		t.Fatalf("error = %q, want it naming the create-temp-file failure", err.Error())
	}
}

// TestWriteFileAtomicRejectsRenameOverADirectory covers writeFileAtomic's
// final os.Rename error branch: renaming a regular temp file onto a path
// that is itself an existing, non-empty DIRECTORY fails deterministically
// and portably (os.Rename refuses file-onto-nonempty-directory on every
// platform this repo supports), matching the class of trigger this
// package's own precedent (internal/cliworktree, internal/chatsync) uses
// for the identical rename-failure shape.
func TestWriteFileAtomicRejectsRenameOverADirectory(t *testing.T) {
	dir := t.TempDir()
	blockerName := "automations.toml"
	blockerDir := filepath.Join(dir, blockerName)
	if err := os.MkdirAll(blockerDir, 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}
	// A non-empty directory: os.Rename onto a directory fails whether or
	// not it is empty on every platform this repo targets, but a
	// non-empty one removes any ambiguity.
	if err := os.WriteFile(filepath.Join(blockerDir, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file inside blocker dir: %v", err)
	}
	if err := writeFileAtomic(dir, blockerName, []byte("data")); err == nil {
		t.Fatal("writeFileAtomic renaming onto a non-empty directory: got nil error, want rejection")
	} else if !strings.Contains(err.Error(), "rename temp file") {
		t.Fatalf("error = %q, want it naming the rename failure", err.Error())
	}
}
