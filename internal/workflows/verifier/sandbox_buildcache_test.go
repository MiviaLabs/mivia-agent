package verifier

import (
	"os"
	"path/filepath"
	"testing"
)

func stubVerifierBuildCacheRoot(t *testing.T, dir string) {
	t.Helper()
	original := verifierBuildCacheRoot
	verifierBuildCacheRoot = func() (string, error) { return dir, nil }
	t.Cleanup(func() { verifierBuildCacheRoot = original })
}

func TestPrepareVerifierBuildCacheCreatesDirectory(t *testing.T) {
	stubVerifierBuildCacheRoot(t, filepath.Join(t.TempDir(), "cache-root"))
	baseline := &GoModuleBaseline{GoMod: []byte("module example.com/a\n")}

	dir, err := prepareVerifierBuildCache(baseline)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("prepareVerifierBuildCache() did not create a directory at %q: %v", dir, err)
	}
}

// TestPrepareVerifierBuildCacheScopesPerProject is the regression this
// exists for: two different projects (different go.mod) must land in
// different cache directories, so one project's sandboxed run cannot plant
// a cache entry a different project's run would trust.
func TestPrepareVerifierBuildCacheScopesPerProject(t *testing.T) {
	stubVerifierBuildCacheRoot(t, filepath.Join(t.TempDir(), "cache-root"))
	a := &GoModuleBaseline{GoMod: []byte("module example.com/a\n")}
	b := &GoModuleBaseline{GoMod: []byte("module example.com/b\n")}

	dirA, err := prepareVerifierBuildCache(a)
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := prepareVerifierBuildCache(b)
	if err != nil {
		t.Fatal(err)
	}
	if dirA == dirB {
		t.Fatalf("two different projects resolved to the same build cache directory: %q", dirA)
	}
}

// TestPrepareVerifierBuildCacheStableAcrossWorktrees is the other half of
// the regression this exists for: the same project must resolve to the
// SAME cache directory across different worktree paths (each workflow run
// gets its own worktree), or the persistence this cache exists for is
// silently defeated.
func TestPrepareVerifierBuildCacheStableAcrossWorktrees(t *testing.T) {
	stubVerifierBuildCacheRoot(t, filepath.Join(t.TempDir(), "cache-root"))
	sameProject := []byte("module example.com/a\n\ngo 1.24\n")

	dir1, err := prepareVerifierBuildCache(&GoModuleBaseline{GoMod: sameProject})
	if err != nil {
		t.Fatal(err)
	}
	dir2, err := prepareVerifierBuildCache(&GoModuleBaseline{GoMod: sameProject})
	if err != nil {
		t.Fatal(err)
	}
	if dir1 != dir2 {
		t.Fatalf("the same project resolved to different build cache directories: %q vs %q", dir1, dir2)
	}
}
