package definition

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
// reclaimed.
//
// Scoped per project (keyed by go.mod content), not shared globally: GOCACHE
// is bound read-write, and Go's cache is content-addressed but not
// integrity-checked against its producer, so an untrusted sandboxed command
// could plant a cache entry under a guessable action ID that a LATER
// verifier run would silently trust instead of recompiling. Keying on
// go.mod content only narrows this (two byte-identical-go.mod projects
// still share a cache dir), not eliminates it — a real poisoning attack
// would also need matching source/toolchain/build flags.
//
// Keyed on go.mod bytes rather than workDir, because a write-capable run
// gets a fresh uniquely-named worktree every time — keying on workDir would
// give every run its own empty cache and defeat the persistence entirely.
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
