package vcs

import (
	"os"
	"strings"
)

// gitEnvRemoved lists environment variables that can redirect or alter git
// behavior. pinnedEnv removes them so every git command in this package runs
// against the repository resolved from cmd.Dir, never against a repository,
// configuration, or identity selected by the parent process environment.
// Mirrors internal/workflows/delivery/gitops.go, with two deliberate
// differences: GIT_SSH and GIT_SSH_COMMAND stay in the environment because
// remote operations need the host-provided SSH transport and workflow agents
// cannot set environment variables themselves; and GIT_CONFIG_GLOBAL /
// GIT_CONFIG_SYSTEM / GIT_CONFIG_COUNT / GIT_CONFIG_PARAMETERS /
// GIT_CONFIG_NOSYSTEM are dropped but never re-pinned, so the user's real
// global and system git configuration (including safe.directory entries) is
// honored instead of being replaced with /dev/null.
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
	"GIT_SSH_VARIANT":                  {},
	"GIT_ASKPASS":                      {},
	"GIT_TERMINAL_PROMPT":              {},
	"GIT_CONFIG_NOSYSTEM":              {},
	"GIT_TEMPLATE_DIR":                 {},
	"GIT_EXEC_PATH":                    {},
	"GIT_NAMESPACE":                    {},
	"GIT_OPTIONAL_LOCKS":               {},
	// Commit identity: the host must own who authors a git commit, so ambient
	// author/committer variables cannot leak into child git processes.
	"GIT_AUTHOR_NAME":     {},
	"GIT_AUTHOR_EMAIL":    {},
	"GIT_AUTHOR_DATE":     {},
	"GIT_COMMITTER_NAME":  {},
	"GIT_COMMITTER_EMAIL": {},
	"GIT_COMMITTER_DATE":  {},
}

// pinnedEnv returns the environment for a git command in this package: the
// caller environment minus every git redirector variable above, re-pinned only
// to a prompt-less git. GIT_TERMINAL_PROMPT=0 is re-pinned so it is the only
// entry for its key; GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM are dropped but
// NOT re-pinned (FINDING E3): re-pinning GIT_CONFIG_GLOBAL=/dev/null and
// GIT_CONFIG_NOSYSTEM=1 suppressed the user's real global/system git config -
// including safe.directory entries - so vcs discovery hard-failed with
// 'detected dubious ownership' on repositories owned by another UID whose
// safe.directory lives in the global config. The drop list alone already
// prevents parent-env config redirection. Unlike the delivery package this
// never sets GIT_DIR or GIT_WORK_TREE: discovery commands (MainRepoRoot,
// RepoRoot, CurrentBranch, ...) run from subdirectories and must still
// discover the repository upward from cmd.Dir. PATH, HOME, and all other
// non-git variables are preserved so PATH shims (tests) and SSH transport keep
// working.
func pinnedEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
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
	return append(env, "GIT_TERMINAL_PROMPT=0")
}
