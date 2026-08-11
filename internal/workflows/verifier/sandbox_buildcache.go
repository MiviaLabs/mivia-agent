package verifier

import (
	"fmt"
	"os"
	"path/filepath"
)

// verifierBuildCacheRoot resolves the persistent host directory bound into
// the sandbox as GOCACHE. It is shared across every workspace and every
// sandboxed invocation: the Go build cache is content-addressed by build
// inputs, so reuse across projects is safe, and it is what turns a cold,
// from-scratch "go test" inside a fresh sandbox root into a warm,
// incremental one on the next call.
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
// directory, ready to bind-mount into the sandbox.
func prepareVerifierBuildCache() (string, error) {
	root, err := verifierBuildCacheRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create verifier build cache: %w", err)
	}
	return root, nil
}
