package agent

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// The operator diagnostic names the hook's ABSOLUTE PATH deliberately - the
// operator is the one who has to go find the file. That is right for a local
// transcript row and wrong for anything that leaves the machine, so the two
// forms are separated at the source rather than by asking a remote boundary to
// spot a path inside free text.

// TestHookStdoutExcludesTheOperatorDiagnostic pins the split at the producer.
func TestHookStdoutExcludesTheOperatorDiagnostic(t *testing.T) {
	run := runtime.HookRun{
		Output:  "policy: no network",
		Warning: "hook /home/operator/private/.mivia/hooks/guard.sh exited 2",
	}

	stdout := hookRunStdout(run)
	if stdout != "policy: no network" {
		t.Errorf("hookRunStdout = %q, want the hook's own output alone", stdout)
	}
	if strings.Contains(stdout, "/home/operator") {
		t.Error("the operator's filesystem path is in the field that leaves the machine")
	}

	// The joined form keeps BOTH, because a local operator wants the
	// diagnostic beside the output.
	joined := hookRunOutput(run)
	if !strings.Contains(joined, "policy: no network") || !strings.Contains(joined, "exited 2") {
		t.Errorf("the local form lost one of its two halves: %q", joined)
	}
}
