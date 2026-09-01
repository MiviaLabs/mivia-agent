package cli

import (
	"testing"

	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestCLI_RegisterSessionBus_BindUnbindRebind exercises the cli-package
// alias (RegisterSessionBus, the replacement for the deleted SetGlobalBus)
// end to end: bind, unbind, and rebind on the SAME session id, through the
// exact symbol dispatchChatSurface calls, asserting against
// clichat.LookupSessionBus so the test proves the alias reaches the SAME
// underlying registry rather than merely "does not panic".
func TestCLI_RegisterSessionBus_BindUnbindRebind(t *testing.T) {
	const id = "cli-alias-session"

	bus1 := events.New()
	t.Cleanup(bus1.Close)
	release1 := RegisterSessionBus(id, bus1)

	if got := clichat.LookupSessionBus(id); got != bus1 {
		t.Fatalf("after bind: LookupSessionBus = %v, want bus1", got)
	}

	release1()
	if got := clichat.LookupSessionBus(id); got != nil {
		t.Fatalf("after unbind: LookupSessionBus = %v, want nil", got)
	}

	bus2 := events.New()
	t.Cleanup(bus2.Close)
	release2 := RegisterSessionBus(id, bus2)
	t.Cleanup(release2)

	if got := clichat.LookupSessionBus(id); got != bus2 {
		t.Fatalf("after rebind: LookupSessionBus = %v, want bus2", got)
	}

	// A stale release for the FIRST binding must not touch the rebind
	// (match-before-delete, exercised through the alias surface).
	release1()
	if got := clichat.LookupSessionBus(id); got != bus2 {
		t.Fatalf("a stale release corrupted the rebind: LookupSessionBus = %v, want bus2", got)
	}

	// release2 called twice must be idempotent (no panic).
	release2()
	release2()
	if got := clichat.LookupSessionBus(id); got != nil {
		t.Fatalf("after final release: LookupSessionBus = %v, want nil", got)
	}
}
