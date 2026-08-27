// Package delivery runs delivery commands against pinned git contexts.
package delivery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitContext pins one git repository context for delivery commands.
type GitContext struct {
	Dir    string // working tree directory
	GitDir string // real git directory (GIT_DIR)
}

// GitRunner executes fixed-argv git commands with a pinned environment.
type GitRunner interface {
	Run(ctx context.Context, gc GitContext, args ...string) (string, error)
}

// RealGit implements GitRunner with exec.CommandContext("git", args...), no shell.
type RealGit struct{}

// gitEnvRemoved lists environment variables that can redirect or alter git
// behavior. pinnedEnv removes them so delivery commands run against the
// pinned GitContext only.
var gitEnvRemoved = map[string]struct{}{
	"GIT_DIR":                          {},
	"GIT_WORK_TREE":                    {},
	"GIT_INDEX_FILE":                   {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_COMMON_DIR":                   {},
	"GIT_CEILING_DIRECTORIES":          {},
	"GIT_CONFIG_GLOBAL":                {},
	"GIT_CONFIG_SYSTEM":                {},
	"GIT_CONFIG_COUNT":                 {},
	"GIT_CONFIG_PARAMETERS":            {},
	"GIT_SSH_COMMAND":                  {},
	"GIT_SSH":                          {},
	"GIT_SSH_VARIANT":                  {},
	"GIT_ASKPASS":                      {},
	"GIT_TERMINAL_PROMPT":              {},
	"GIT_TEMPLATE_DIR":                 {},
	"GIT_EXEC_PATH":                    {},
	"GIT_NAMESPACE":                    {},
	"GIT_OPTIONAL_LOCKS":               {},
	// Commit identity: the host must own who authors a delivery commit, so
	// ambient author/committer variables cannot leak into it.
	"GIT_AUTHOR_NAME":     {},
	"GIT_AUTHOR_EMAIL":    {},
	"GIT_AUTHOR_DATE":     {},
	"GIT_COMMITTER_NAME":  {},
	"GIT_COMMITTER_EMAIL": {},
	"GIT_COMMITTER_DATE":  {},
	// Locale: a translated git message ("konnte ... nicht finden") would
	// break the delivery package's English-message classification of fetch
	// failures ("couldn't find remote ref"), so a permanently deleted base
	// would be treated as recoverable. LANGUAGE is stripped too: GNU gettext
	// gives a non-empty LANGUAGE priority over LC_ALL (glibc short-circuits it
	// at the C locale, non-glibc libintl does not), so a caller's LANGUAGE
	// could still translate git's messages. Locale is pinned with LC_ALL=C in
	// pinnedEnv; these strips make that appended entry the only one left.
	"LC_ALL":      {},
	"LC_MESSAGES": {},
	"LANG":        {},
	"LANGUAGE":    {},
}

// pinnedEnv returns the environment for a pinned git command: the caller
// environment minus all git-affecting variables, plus the pinned context
// variables that force git to use gc.Dir and gc.GitDir and the locale pin
// that forces English git messages.
func pinnedEnv(gc GitContext) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		name, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if _, removed := gitEnvRemoved[name]; removed {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_DIR="+gc.GitDir,
		"GIT_WORK_TREE="+gc.Dir,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LC_ALL=C",
	)
}

// Run executes git with args in the pinned context. It returns the combined
// stdout and stderr. On failure it returns the output and a wrapped error
// that includes the git stderr.
func (RealGit) Run(ctx context.Context, gc GitContext, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = gc.Dir
	cmd.Env = pinnedEnv(gc)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(text))
		}
		return text, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// VerifyGitDir validates the worktree's .git file and returns the real git
// directory. It refuses a missing, symlinked, or misdirected .git file.
func VerifyGitDir(ctx context.Context, mainRoot, worktreeName, worktreeDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dotGit := filepath.Join(worktreeDir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", fmt.Errorf("verify git dir %s: %w", dotGit, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("verify git dir %s: .git is a symlink, refusing", dotGit)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("verify git dir %s: .git is not a regular file", dotGit)
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", fmt.Errorf("verify git dir %s: read .git: %w", dotGit, err)
	}
	rest := strings.TrimPrefix(string(data), "gitdir: ")
	if rest == string(data) {
		return "", fmt.Errorf("verify git dir %s: .git must start with %q", dotGit, "gitdir: ")
	}
	rest = strings.TrimSuffix(rest, "\n")
	rest = strings.TrimSuffix(rest, "\r")
	if rest == "" {
		return "", fmt.Errorf("verify git dir %s: .git has an empty gitdir path", dotGit)
	}
	gitDir := rest
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreeDir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	want := filepath.Clean(filepath.Join(mainRoot, ".git", "worktrees", worktreeName))
	if gitDir != want {
		return "", fmt.Errorf("verify git dir %s: gitdir points to %q, want %q", dotGit, gitDir, want)
	}
	stat, err := os.Stat(gitDir)
	if err != nil {
		return "", fmt.Errorf("verify git dir %s: git dir %q: %w", dotGit, gitDir, err)
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("verify git dir %s: git dir %q is not a directory", dotGit, gitDir)
	}
	// A symlink inside the path can make the lexical path and the physical
	// path disagree. Resolve both and require exact equality so a crafted
	// gitdir value cannot redirect git to a different repository.
	physical, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return "", fmt.Errorf("verify git dir %s: resolve git dir %q: %w", dotGit, gitDir, err)
	}
	expected, err := filepath.EvalSymlinks(want)
	if err != nil {
		return "", fmt.Errorf("verify git dir %s: resolve expected git dir %q: %w", dotGit, want, err)
	}
	if physical != expected {
		return "", fmt.Errorf("verify git dir %s: git dir %q resolves to %q, want %q", dotGit, gitDir, physical, expected)
	}
	return gitDir, nil
}
