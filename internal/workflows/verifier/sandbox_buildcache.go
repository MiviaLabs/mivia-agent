package verifier

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// projectBuildCachePrefix names every per-project cache directory this
// package creates under verifierBuildCacheRoot, so a stale one can be
// recognised later (mirrors sandboxRootPrefix in sandbox_sweep.go).
const projectBuildCachePrefix = "proj-"

// staleBuildCacheAge is how long a project's cache directory can go unused
// before it is swept. Unlike a sandbox root (one bounded command, hours),
// a project's build cache is meant to persist across many runs over the
// project's active lifetime - this only reclaims disk from a project that
// has genuinely stopped being verified, not a slow one.
const staleBuildCacheAge = 30 * 24 * time.Hour

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
// sandbox as GOCACHE. It sweeps stale sibling directories first and marks
// this one as just-used, so an abandoned project's cache is eventually
// reclaimed without needing an operator to notice.
//
// It is scoped per project (keyed by the admitted go.mod content), not
// shared globally across every project verified on the host. Unlike the
// read-only module cache mount, GOCACHE is bound read-write: a sandboxed
// command that runs untrusted or agent-authored code could otherwise plant
// a cache entry under a guessable action ID (Go's cache is content-
// addressed, not integrity-checked against its producer) that a LATER
// verifier run sharing this directory would silently trust instead of
// recompiling - exactly the kind of cross-invocation host side effect the
// sandbox exists to prevent. Scoping per project narrows that blast radius,
// but the key is go.mod content equality, not verified project identity:
// two genuinely different codebases that happen to ship byte-identical
// go.mod (e.g. two minimal/template projects with no dependencies) would
// still share a cache directory. A real poisoning attack against such a
// pair would additionally need the victim's actual source, toolchain, and
// build flags to match, which go.mod content alone does not determine, so
// this narrows rather than eliminates the risk the global-cache version had.
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
	cacheRoot, err := verifierBuildCacheRoot()
	if err != nil {
		return "", err
	}
	now := time.Now()
	sweepStaleBuildCaches(cacheRoot, now, staleBuildCacheAge)
	root := filepath.Join(cacheRoot, projectBuildCacheKey(baseline))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create verifier build cache: %w", err)
	}
	// Go reuse of an existing cache is read-heavy and does not necessarily
	// touch this directory's own mtime, so an explicit touch is the "used
	// at" signal the sweep relies on - without it, a project verified
	// often but whose cache entries are mostly reads would look idle and
	// eventually get swept out from under it.
	_ = os.Chtimes(root, now, now)
	return root, nil
}

// projectBuildCacheKey derives a stable, filesystem-safe subdirectory name
// for one project's build cache from its admitted go.mod.
func projectBuildCacheKey(baseline *GoModuleBaseline) string {
	h := sha256.Sum256(baseline.GoMod)
	return projectBuildCachePrefix + fmt.Sprintf("%x", h[:8])
}

// sweepStaleBuildCaches removes per-project cache directories that have not
// been used in staleBuildCacheAge. Errors are deliberately ignored: this is
// opportunistic housekeeping in front of the real work, and a directory
// owned by another user or racing with another sweep must never fail the
// verification the caller asked for (mirrors sweepStaleSandboxRoots).
func sweepStaleBuildCaches(dir string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), projectBuildCachePrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}
