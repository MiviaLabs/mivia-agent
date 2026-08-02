package workspace

import (
	"path/filepath"
	"testing"
)

func TestContextStorePathUsesWorkspaceNamespace(t *testing.T) {
	root := filepath.Join("tmp", "workspace")
	if got, want := ContextStorePath(root), filepath.Join(root, Namespace, "context.db"); got != want {
		t.Fatalf("ContextStorePath(%q) = %q, want %q", root, got, want)
	}
}
