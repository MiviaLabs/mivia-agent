package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pinnedEnvFixture pins a full ambient git environment - every redirector and
// identity variable, an ambient GIT_TERMINAL_PROMPT, ambient GIT_CONFIG_* pins,
// plus host SSH/PATH/HOME and an unrelated non-git variable - then returns the
// pinnedEnv() result, a map view of it, and the pin lists so tests can assert
// the DC-10 contract: every git redirector and identity variable is dropped,
// GIT_SSH/GIT_SSH_COMMAND and PATH/HOME and non-git variables are kept, the
// prompt pin is re-applied as the only entry for its key, and GIT_WORK_TREE is
// never set (discovery commands must still resolve the repository upward from
// cmd.Dir). GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM must NOT be re-pinned:
// the old GIT_CONFIG_GLOBAL=/dev/null + GIT_CONFIG_NOSYSTEM=1 pins suppressed
// the user's real global/system git config (FINDING E3), including
// safe.directory entries, so vcs discovery hard-failed with 'detected dubious
// ownership' on repositories owned by another UID. The drop list still strips
// ambient values; the user's real config must be honored.
func pinnedEnvFixture(t *testing.T) (redirectors []string, repinned map[string]string, unsetPins []string, env []string, got map[string]string) {
	t.Helper()
	// Redirector and identity variables that must never survive into a child
	// git process.
	redirectors = []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_NAMESPACE",
		"GIT_TEMPLATE_DIR", "GIT_EXEC_PATH", "GIT_OPTIONAL_LOCKS", "GIT_ASKPASS",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
		"GIT_SSH_VARIANT",
	}
	// Re-pinned variables: their ambient value must be dropped and replaced by
	// exactly one entry with the forced value, so the ambient value cannot win
	// through a duplicate environment entry (which resolves platform-dependently).
	repinned = map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
	}
	// Config pins removed by FINDING E3: pinnedEnv must not re-apply
	// GIT_CONFIG_GLOBAL=/dev/null or GIT_CONFIG_NOSYSTEM=1, which suppressed
	// the user's real global/system git config and broke vcs discovery on
	// repositories owned by another UID (safe.directory lives in the global
	// config). The drop list still strips ambient values.
	unsetPins = []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"}
	for _, name := range redirectors {
		t.Setenv(name, "ambient-"+name)
	}
	for name, value := range repinned {
		t.Setenv(name, "ambient-"+value)
	}
	t.Setenv("GIT_SSH", "/usr/bin/ssh-wrapper")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /home/user/key")
	t.Setenv("PATH", "/pinned/path:/usr/bin")
	t.Setenv("HOME", "/home/user")
	t.Setenv("MIVIA_UNRELATED", "keep-me")

	env = pinnedEnv()
	return redirectors, repinned, unsetPins, env, mapEnv(env)
}

// TestPinnedEnvDropsRedirectors pins the DC-10 drop half of the pinnedEnv
// contract: every git redirector and identity variable must be gone from the
// child environment, the drop list must itself cover each one plus the
// re-pinned prompt and the E3 config pins, and the config pins must stay
// absent so git honors the user's real global/system configuration.
func TestPinnedEnvDropsRedirectors(t *testing.T) {
	redirectors, _, unsetPins, env, got := pinnedEnvFixture(t)

	// Every non-re-pinned redirector must be gone from the pinned env.
	for _, name := range redirectors {
		if value, ok := got[name]; ok {
			t.Errorf("pinnedEnv keeps %s=%q", name, value)
		}
	}
	// The drop list must itself reject every redirector and re-pinned variable;
	// if a future edit removes one it would re-enter through os.Environ.
	for _, name := range append(append(append([]string{}, redirectors...), "GIT_TERMINAL_PROMPT"), unsetPins...) {
		if _, ok := gitEnvRemoved[name]; !ok {
			t.Errorf("gitEnvRemoved is missing %s", name)
		}
	}
	// Unset config pins: exactly zero entries, so git honors the user's real
	// global/system configuration instead of /dev/null.
	for _, name := range unsetPins {
		if count := countEnv(env, name); count != 0 {
			t.Errorf("pinnedEnv sets %s (count=%d), want absent so the user's real git config is honored", name, count)
		}
	}
}

// TestPinnedEnvRepinsPromptAndKeepsNonGit pins the re-pin and keep half of the
// pinnedEnv contract: the prompt pin is re-applied as exactly one entry with
// its forced value (the ambient value cannot win through a duplicate entry),
// while SSH transport, PATH/HOME, and unrelated non-git variables come through
// from the host environment unchanged.
func TestPinnedEnvRepinsPromptAndKeepsNonGit(t *testing.T) {
	_, repinned, _, env, got := pinnedEnvFixture(t)

	// Re-pinned variables: exactly one entry, with the pinned value.
	for name, want := range repinned {
		if count := countEnv(env, name); count != 1 {
			t.Errorf("pinnedEnv has %d entries for %s, want exactly 1", count, name)
		}
		if got[name] != want {
			t.Errorf("%s = %q, want re-pinned %q", name, got[name], want)
		}
	}
	// SSH transport comes from the host environment: remote operations need
	// it, and workflow agents cannot set environment variables themselves.
	if got["GIT_SSH"] != "/usr/bin/ssh-wrapper" {
		t.Errorf("GIT_SSH = %q, want host value kept", got["GIT_SSH"])
	}
	if got["GIT_SSH_COMMAND"] != "ssh -i /home/user/key" {
		t.Errorf("GIT_SSH_COMMAND = %q, want host value kept", got["GIT_SSH_COMMAND"])
	}
	// PATH (fake-git shims in tests) and HOME plus unrelated variables stay.
	if got["PATH"] != "/pinned/path:/usr/bin" {
		t.Errorf("PATH = %q, want kept", got["PATH"])
	}
	if got["HOME"] != "/home/user" {
		t.Errorf("HOME = %q, want kept", got["HOME"])
	}
	if got["MIVIA_UNRELATED"] != "keep-me" {
		t.Errorf("non-git variable = %q, want kept", got["MIVIA_UNRELATED"])
	}
}

// TestPinnedEnvHonorsGlobalConfig is the FINDING E3 behavior regression: a git
// spawn under pinnedEnv must observe the user's real global configuration. The
// old GIT_CONFIG_GLOBAL=/dev/null + GIT_CONFIG_NOSYSTEM=1 re-pins suppressed it
// - including safe.directory entries - so vcs discovery (MainRepoRoot,
// RepoRoot, ensureGitRepo, DetectBranch, IsWorktree, List, Resolve) hard-failed
// with 'detected dubious ownership' on repositories owned by another UID whose
// safe.directory lives in the global config. With HOME pointing at a temp dir
// containing a global .gitconfig, a pinned git spawn must read user.name from
// it.
func TestPinnedEnvHonorsGlobalConfig(t *testing.T) {
	skipIfNoGit(t)
	home := t.TempDir()
	write(t, home, ".gitconfig", "[user]\n\tname = global-user\n")
	t.Setenv("HOME", home)
	// Keep the test hermetic: XDG must not leak a second global config, and
	// ambient GIT_CONFIG_* redirection must be stripped by the drop list.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_NOSYSTEM"} {
		t.Setenv(name, "ambient-"+name)
	}

	cmd := exec.Command("git", "config", "--global", "--get", "user.name")
	cmd.Env = pinnedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --global --get user.name: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "global-user" {
		t.Fatalf("global config user.name = %q, want %q (pinnedEnv must not suppress GIT_CONFIG_GLOBAL)", got, "global-user")
	}
}

// TestMainRepoRootIgnoresAmbientGitDir is the DC-10 regression test: a
// workflow launched from a git hook or CI job inherits GIT_DIR and
// GIT_WORK_TREE pointing at a different repository. MainRepoRoot must still
// resolve the repository that contains dir, not the one named by the
// environment. Fails before the pinnedEnv fix.
func TestMainRepoRootIgnoresAmbientGitDir(t *testing.T) {
	source := initTestRepo(t)
	other := initTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	got, err := MainRepoRoot(source)
	if err != nil {
		t.Fatalf("MainRepoRoot: %v", err)
	}
	abs, _ := filepath.Abs(source)
	if got != abs {
		t.Errorf("MainRepoRoot = %q, want source repo %q (ambient GIT_DIR must not redirect)", got, abs)
	}
}

// TestMainRepoRootFromSubdirIgnoresAmbientGitDir pins the discovery contract:
// MainRepoRoot runs from a subdirectory (never with GIT_WORK_TREE forced) and
// must still discover the repository upward, even with ambient GIT_DIR set.
func TestMainRepoRootFromSubdirIgnoresAmbientGitDir(t *testing.T) {
	source := initTestRepo(t)
	sub := filepath.Join(source, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := initTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	got, err := MainRepoRoot(sub)
	if err != nil {
		t.Fatalf("MainRepoRoot from subdir: %v", err)
	}
	abs, _ := filepath.Abs(source)
	if got != abs {
		t.Errorf("MainRepoRoot from subdir = %q, want source repo %q", got, abs)
	}
}

// TestRepoRootIgnoresAmbientGitDir is the DC-10 regression test for RepoRoot.
func TestRepoRootIgnoresAmbientGitDir(t *testing.T) {
	source := initTestRepo(t)
	other := initTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	got, err := RepoRoot(source)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	abs, _ := filepath.Abs(source)
	if got != abs {
		t.Errorf("RepoRoot = %q, want source repo %q (ambient GIT_DIR must not redirect)", got, abs)
	}
}

// TestCreateRemoveIgnoresAmbientGitDir is the DC-10 regression test for the
// mutation path: worktree creation and removal must operate on the repo at
// repoRoot, never on the repo named by an inherited GIT_DIR. Cleanup removing
// a same-named wf/* worktree from another repository is the concrete escape
// this closes. Fails before the pinnedEnv fix.
func TestCreateRemoveIgnoresAmbientGitDir(t *testing.T) {
	source := initTestRepo(t)
	other := initTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))

	ctx := context.Background()
	wt, err := Create(ctx, source, "ambient-wt", "HEAD")
	if err != nil {
		t.Fatalf("Create with ambient GIT_DIR: %v", err)
	}

	// Git registers a linked worktree under <repo>/.git/worktrees/<name>.
	// With the inherited GIT_DIR the registration lands in the other repo and
	// this stat fails.
	registered := filepath.Join(source, ".git", "worktrees", wt.Name)
	if _, err := os.Stat(registered); err != nil {
		t.Fatalf("worktree %q is not registered in the source repo at %s: %v", wt.Name, registered, err)
	}

	found, err := Resolve(ctx, source, "ambient-wt")
	if err != nil || found == nil {
		t.Fatalf("Resolve(source, ambient-wt) = %+v, %v; want the source worktree", found, err)
	}

	if err := Remove(ctx, source, "ambient-wt"); err != nil {
		t.Fatalf("Remove with ambient GIT_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, ".mivia", "worktrees", "ambient-wt")); !os.IsNotExist(err) {
		t.Fatalf("worktree path still exists after Remove: %v", err)
	}
	if _, err := os.Stat(registered); !os.IsNotExist(err) {
		t.Fatalf("worktree registration still exists after Remove: %v", err)
	}
}

func mapEnv(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			m[name] = value
		}
	}
	return m
}

func countEnv(env []string, name string) int {
	count := 0
	for _, kv := range env {
		if n, _, ok := strings.Cut(kv, "="); ok && n == name {
			count++
		}
	}
	return count
}
