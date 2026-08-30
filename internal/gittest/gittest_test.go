package gittest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDisableDetachedMaintenanceReachesSpawnedGit proves the environment
// pairs actually land in a spawned git's effective configuration - the
// whole mechanism rests on this plumbing, so it is pinned against a real
// git process, not inspected.
func TestDisableDetachedMaintenanceReachesSpawnedGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_COUNT", "")
	DisableDetachedMaintenance()
	dir := t.TempDir()
	out, err := exec.Command("git", "init", "-q", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for key, want := range map[string]string{
		"gc.auto":          "0",
		"gc.autoDetach":    "false",
		"receive.autogc":   "false",
		"maintenance.auto": "false",
	} {
		cmd := exec.Command("git", "-C", dir, "config", "--get", key)
		got, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("config --get %s: %v\n%s", key, err, got)
		}
		if strings.TrimSpace(string(got)) != want {
			t.Fatalf("%s = %q, want %q", key, strings.TrimSpace(string(got)), want)
		}
	}
	if os.Getenv("GIT_CONFIG_COUNT") != "4" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 4", os.Getenv("GIT_CONFIG_COUNT"))
	}
	// Composition: a second call appends after existing pairs.
	DisableDetachedMaintenance()
	if os.Getenv("GIT_CONFIG_COUNT") != "8" {
		t.Fatalf("composed GIT_CONFIG_COUNT = %q, want 8", os.Getenv("GIT_CONFIG_COUNT"))
	}
}
