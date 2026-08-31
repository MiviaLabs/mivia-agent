package hub

import (
	"strings"
	"testing"
)

// TestHubPipeSDDLGrantsOnlyOwnerAndSystem is the Windows half of the hub's
// access control, asserted on every platform.
//
// The Unix listener chmods the socket to 0600, so only the owning user can
// connect. The Windows listener passed a nil PipeConfig, which makes go-winio
// fall back to the system default named-pipe DACL - and that grants read access
// to every local user. The pipe name is a plain hash of a guessable store path
// and owner.accept authenticates nothing, so any local process could connect
// and receive every session's turn_start Detail (the user's own prompt),
// assistant text, thinking, and tool input/output.
//
// This test lives in a file with no build tag, and the descriptor lives beside
// it in socket_acl.go for the same reason: a Windows-only constant is a
// constant nobody on this project can run a check against, and it was the
// absence of any check that let the two platforms diverge.
func TestHubPipeSDDLGrantsOnlyOwnerAndSystem(t *testing.T) {
	got := hubPipeSDDL

	if !strings.HasPrefix(got, "D:P") {
		t.Fatalf("SDDL = %q, want a PROTECTED DACL (D:P...); without P the pipe inherits ACEs from elsewhere", got)
	}
	for _, want := range []string{"(A;;GA;;;OW)", "(A;;GA;;;SY)"} {
		if !strings.Contains(got, want) {
			t.Errorf("SDDL = %q, missing the %s grant", got, want)
		}
	}
	// The SIDs that would reopen the hole. WD is Everyone, AU is Authenticated
	// Users, BU is Builtin Users, IU is Interactive - each of them is "some
	// other local account can read this user's prompts".
	for _, forbidden := range []string{";WD)", ";AU)", ";BU)", ";IU)", ";BG)", ";AN)"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("SDDL = %q grants %s; a local user other than the owner can read every relayed prompt", got, forbidden)
		}
	}
	if strings.Contains(got, "(A;;") && strings.Count(got, "(A;;") > 2 {
		t.Errorf("SDDL = %q grants more than owner and SYSTEM", got)
	}
}
