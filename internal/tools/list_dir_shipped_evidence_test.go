package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestDriveListDirShippedEvidence drives list_dir through NewDefaultRegistry
// (the production composition) on a representative fixture: recursive tree with
// sizes + ignore markers, and depth-1 flat listing without sizes.
func TestDriveListDirShippedEvidence(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "target", "debug"), 0o755))
	must(os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("target/\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "src", "main.rs"), []byte("fn main() {}\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "target", "debug", "app"), []byte("bin"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644))

	ws, err := workspace.Open(dir)
	must(err)
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})

	out, err := reg.Execute(context.Background(), "list_dir", json.RawMessage(`{"path":".","depth":3}`))
	must(err)
	t.Logf("recursive:\n%s", out)
	for _, want := range []string{
		"target/  (ignored)",
		"node_modules/  (ignored)",
		"src/",
		"main.rs  13",
		"README.md  6",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recursive missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "debug") || strings.Contains(out, "index.js") {
		t.Errorf("must not descend ignored dirs:\n%s", out)
	}

	out1, err := reg.Execute(context.Background(), "list_dir", json.RawMessage(`{"path":"."}`))
	must(err)
	t.Logf("depth1:\n%s", out1)
	// Depth-1: names only, trailing slash on dirs, no sizes, no (ignored) markers.
	if strings.Contains(out1, "(ignored)") || strings.Contains(out1, "  6") {
		t.Errorf("depth-1 must stay flat historical form:\n%s", out1)
	}
	for _, want := range []string{".gitignore", "README.md", "node_modules/", "src/", "target/"} {
		if !strings.Contains(out1, want) {
			t.Errorf("depth-1 missing %q in:\n%s", want, out1)
		}
	}

	// Optional: dump for goal harness evidence capture.
	if path := os.Getenv("LIST_DIR_EVIDENCE"); path != "" {
		body := "=== recursive depth=3 ===\n" + out + "\n=== depth1 golden path ===\n" + out1 + "\n"
		must(os.WriteFile(path, []byte(body), 0o644))
	}
}
