package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooksession"
)

// Pure hook-session logic (discovery, listing, notices) is tested in
// internal/hooksession now, which owns that logic. This file keeps only the
// tests that exercise the cli-layer wiring: the /hooks route through the
// classic slash dispatcher, and unrelated chat-flag parsing.

// /hooks must be reachable and discoverable on both surfaces. A command that
// exists but is not routed reports "unknown command", which is how a user
// concludes hooks have no UI at all.
func TestHooksSlashIsRoutedAndListed(t *testing.T) {
	_, ws := hookHome(t, twoHookConfig)
	session, err := hooksession.Load(ws)
	if err != nil {
		t.Fatalf("hooksession.Load: %v", err)
	}
	restore := hooksession.SetForTest(session)
	t.Cleanup(restore)

	handled, _, err := handleSlash("/hooks", nil, nil, true, nil)
	if err != nil {
		t.Fatalf("handleSlash: %v", err)
	}
	if !handled {
		t.Fatal("/hooks must be routed in the classic dispatcher")
	}

	var found bool
	for _, command := range builtInSlashCommands() {
		if command.Name == "/hooks" {
			found = true
			if command.Surface != slashSurfaceBoth {
				t.Errorf("/hooks must be offered on both surfaces, got %v", command.Surface)
			}
		}
	}
	if !found {
		t.Fatal("/hooks must appear in the slash catalog")
	}
}

// A stale --bypass-hook-trust in a CI config must not fail the run, and must
// not be silently swallowed either: the operator needs to know it stopped
// meaning anything.
func TestStaleBypassFlagIsAcceptedAndReported(t *testing.T) {
	noTools, plainUI, staleBypass, _, _, _, _, rest := chatFlags([]string{"--bypass-hook-trust", "keep"})
	if !staleBypass {
		t.Fatal("the removed flag must still parse rather than land in rest")
	}
	if noTools || plainUI {
		t.Fatal("the removed flag must not set unrelated flags")
	}
	if len(rest) != 1 || rest[0] != "keep" {
		t.Fatalf("other arguments must survive, got %v", rest)
	}
}

// --quiet must parse as a chat flag and suppress the startup notices.
func TestQuietFlagIsAccepted(t *testing.T) {
	noTools, plainUI, _, _, quiet, _, _, rest := chatFlags([]string{"--quiet", "keep"})
	if !quiet {
		t.Fatal("--quiet must parse as a chat flag")
	}
	if noTools || plainUI {
		t.Fatal("--quiet must not set unrelated flags")
	}
	if len(rest) != 1 || rest[0] != "keep" {
		t.Fatalf("other arguments must survive, got %v", rest)
	}
}
