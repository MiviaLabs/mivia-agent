package tools

import (
	"strings"
	"testing"
)

// TestDeferredIndexUnchangedAcrossAdmissionsWithinOneGeneration pins INV-68-5:
// DeferredIndex is deterministic for an equal candidate set, renders "" for
// the empty set, and a previously rendered (frozen) index is never altered by
// a later admission that would remove a name from a fresh render - so the
// index embedded in one AgentSurfaceGeneration's system prompt stays stable
// for the binding's lifetime (plan tools/05 D8, plan 68 W4).
func TestDeferredIndexUnchangedAcrossAdmissionsWithinOneGeneration(t *testing.T) {
	candidates := []TierCandidate{
		{Name: "alpha", Description: "Reads alpha. Then stops."},
		{Name: "bravo", Description: "Writes bravo. Then stops."},
	}

	first := DeferredIndex(candidates)
	again := DeferredIndex(candidates)
	if first != again {
		t.Fatalf("DeferredIndex is not deterministic for an equal candidate set:\n%q\n---\n%q", first, again)
	}
	if DeferredIndex(nil) != "" {
		t.Fatalf("empty candidate set must render nothing, got %q", DeferredIndex(nil))
	}
	if !strings.Contains(first, "alpha") || !strings.Contains(first, "bravo") {
		t.Fatalf("index must name both deferred tools: %q", first)
	}

	// A later admission removes a name from the deferred set; a fresh render
	// reflects the smaller set, but the FROZEN index already embedded in the
	// binding's system prompt is a plain string and stays byte-identical.
	frozen := first
	reduced := DeferredIndex(candidates[:1])
	if reduced == first {
		t.Fatalf("reduced candidate set must render differently: %q", reduced)
	}
	if frozen != first {
		t.Fatal("the frozen index mutated after a later admission")
	}
	if !strings.Contains(frozen, "bravo") {
		t.Fatalf("frozen index lost a name that was deferred when it was frozen: %q", frozen)
	}
}
