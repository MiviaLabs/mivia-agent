//go:build windows

package vcs

import (
	"os"
	"os/exec"
	"testing"
)

func TestRunGitMutationRejectsInvalidLeaseHandle(t *testing.T) {
	lease, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := runGitMutation(exec.Command("cmd", "/c", "exit", "0"), lease)
	if err == nil {
		t.Fatal("runGitMutation with a closed lease succeeded")
	}
	if output != nil {
		t.Fatalf("output = %q, want nil", output)
	}
}
