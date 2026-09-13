package cliautomations

// GetOrResumeInDir's failure arms. Resume is how a run picks a crashed or
// interrupted session back up, so a missing or unloadable session must
// surface as a named error rather than a nil session the caller would
// dereference on its first turn.

import (
	"strings"
	"testing"
)

// TestGetOrResumeInDirFailsOnAnUnknownSessionID covers the Load error wrap:
// resuming an id that was never saved must report that id, not a generic
// construction failure, because the operator's next step is to look it up.
func TestGetOrResumeInDirFailsOnAnUnknownSessionID(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	conv, sess, err := spawn.GetOrResumeInDir("no-such-session-id", "")
	if err == nil {
		t.Fatal("GetOrResumeInDir with an unknown id succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-session-id") {
		t.Fatalf("error = %v, want it to name the session id that could not be loaded", err)
	}
	if conv != nil || sess != nil {
		t.Fatalf("a failed resume returned conv=%v sess=%v, want both nil", conv, sess)
	}
}

// The success path of GetOrResumeInDir is covered end to end by
// internal/automation's resume tests, which drive a real saved run through
// the same spawner; this file pins only the failure arm, which those tests
// never reach.
