//go:build !windows

package memory

import (
	"path/filepath"
	"testing"
)

// TestSyncMemoryDir_OpenErrorSurfaces pins syncMemoryDir's own os.Open
// error wrap: a missing directory must surface the error rather than a
// silent success.
func TestSyncMemoryDir_OpenErrorSurfaces(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := syncMemoryDir(missing); err == nil {
		t.Fatal("syncMemoryDir accepted a missing directory")
	}
}
