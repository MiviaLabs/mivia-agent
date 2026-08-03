package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/version"
)

func TestVersion(t *testing.T) {
	if err := cli.Execute([]string{"version"}); err != nil {
		t.Fatal(err)
	}
}

// TestVersionPrintsRender asserts `mivia version` prints the provenance-aware
// render from internal/version. Tests run without -ldflags, so this exercises
// the fallback path: plain "mivia <version>" with no commit parenthetical.
func TestVersionPrintsRender(t *testing.T) {
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = outW
	runErr := cli.Execute([]string{"version"})
	os.Stdout = oldOut
	_ = outW.Close()
	stdout, _ := io.ReadAll(outR)

	if runErr != nil {
		t.Fatalf("Execute(version): %v", runErr)
	}
	want := version.String() + "\n"
	if string(stdout) != want {
		t.Fatalf("version output = %q, want %q", stdout, want)
	}
}

func TestHelpDoesNotWriteWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	if err := run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", ".ai"} {
		if _, err := os.Stat(filepath.Join(dir, path)); !os.IsNotExist(err) {
			t.Fatalf("help created %s, stat err=%v", path, err)
		}
	}
}

func TestHelp(t *testing.T) {
	if err := cli.Execute([]string{"help"}); err != nil {
		t.Fatal(err)
	}
}

func TestUnknown(t *testing.T) {
	if err := cli.Execute([]string{"not-a-command"}); err == nil {
		t.Fatal("expected error")
	}
}
