package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

func TestVersion(t *testing.T) {
	if err := cli.Execute([]string{"version"}); err != nil {
		t.Fatal(err)
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
