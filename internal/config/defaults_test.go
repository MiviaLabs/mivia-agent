package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestTempStorePath pins TempStorePath's contract: deterministic per
// (root, name), rooted at os.TempDir(), namespaced under
// workspace.Namespace, and distinct roots never collide.
func TestTempStorePath(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "a")
	rootB := filepath.Join(t.TempDir(), "b")

	got1 := TempStorePath(rootA, "memory")
	got2 := TempStorePath(rootA, "memory")
	if got1 != got2 {
		t.Fatalf("TempStorePath must be deterministic: %q != %q", got1, got2)
	}

	gotB := TempStorePath(rootB, "memory")
	if gotB == got1 {
		t.Fatalf("distinct roots must yield distinct paths, got %q for both", got1)
	}

	if !strings.HasPrefix(got1, os.TempDir()) {
		t.Fatalf("TempStorePath must be rooted at os.TempDir(): %q", got1)
	}
	if !strings.Contains(got1, workspace.Namespace) {
		t.Fatalf("TempStorePath must be namespaced under workspace.Namespace: %q", got1)
	}
	if !strings.HasSuffix(got1, "memory.db") {
		t.Fatalf("TempStorePath must end in name+\".db\": %q", got1)
	}
}
