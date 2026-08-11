package chat

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrefixResetAuthorityUnchanged pins INV-68-4 (finding R0-1): PrefixIdentity
// and KindPrefixReset are read-only observability. No production .go file under
// internal/agent, internal/runtime, internal/hooks, internal/secretpath, or
// internal/config, and no part of internal/tools/scope.go, may reference them:
// they are never consulted by runtime.Dispatcher, ScopedRegistry, or any
// authorization check. The scan excludes test files, so this test itself does
// not trip it.
func TestPrefixResetAuthorityUnchanged(t *testing.T) {
	scanDirs := []string{"agent", "runtime", "hooks", "secretpath", "config"}
	for _, dir := range scanDirs {
		root := filepath.Join("..", dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(data), "PrefixIdentity") || strings.Contains(string(data), "KindPrefixReset") {
				t.Fatalf("authority surface %s references prefix observability (INV-68-4)", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scanning %s: %v", root, err)
		}
	}

	scope := filepath.Join("..", "tools", "scope.go")
	data, err := os.ReadFile(scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PrefixIdentity") || strings.Contains(string(data), "KindPrefixReset") {
		t.Fatal("internal/tools/scope.go must not reference prefix observability (INV-68-4)")
	}
}
