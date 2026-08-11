package verifier

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// verifierBuildCacheRoot resolves the persistent host directory that holds
// every workspace's sandboxed GOCACHE.
//
// It deliberately does not live under os.TempDir()/TMPDIR: a temp directory
// is wiped on reboot, and on some hosts is RAM-backed tmpfs, either of which
// would silently defeat persistence. os.UserCacheDir() is the
// platform-appropriate persistent cache location on Linux, macOS, and
// Windows alike (mirrors DefaultStorePathForWorkspace in internal/config).
var verifierBuildCacheRoot = func() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve verifier build cache directory: %w", err)
	}
	return filepath.Join(dir, "mivia", "verifier-gocache"), nil
}

// prepareVerifierBuildCache resolves and creates the persistent build cache
// directory for one project, ready to bind-mount read-write into the
// sandbox as GOCACHE.
//
// It is scoped per project (keyed by the admitted go.mod content), not
// shared globally across every project verified on the host. Unlike the
// read-only module cache mount, GOCACHE is bound read-write: a sandboxed
// command that runs untrusted or agent-authored code could otherwise plant
// a cache entry under a guessable action ID (Go's cache is content-
// addressed, not integrity-checked against its producer) that a LATER,
// unrelated project's verifier run would silently trust instead of
// recompiling - exactly the kind of cross-invocation host side effect the
// sandbox exists to prevent. Scoping per project keeps that blast radius
// inside one project's own repeated runs.
//
// The key is the baseline go.mod bytes, not workDir: a write-capable
// workflow run gets a fresh, uniquely-named Git worktree per run
// (workflowspace.Identity.Root), so workDir differs on every run of the
// same project - keying on it would give every run its own empty cache and
// silently defeat the persistence this exists for. go.mod content is
// stable across a project's worktrees and only changes when dependencies
// actually do, which is exactly when a fresh cache namespace is
// appropriate anyway.
func prepareVerifierBuildCache(baseline *GoModuleBaseline) (string, error) {
	root, err := verifierBuildCacheRoot()
	if err != nil {
		return "", err
	}
	root = filepath.Join(root, projectBuildCacheKey(baseline))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create verifier build cache: %w", err)
	}
	return root, nil
}

// projectBuildCacheKey derives a stable, filesystem-safe subdirectory name
// for one project's build cache from its admitted go.mod.
func projectBuildCacheKey(baseline *GoModuleBaseline) string {
	h := sha256.Sum256(baseline.GoMod)
	return fmt.Sprintf("proj-%x", h[:8])
}
