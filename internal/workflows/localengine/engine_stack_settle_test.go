package localengine

// Direct unit tests for stackHasProgress: the agent-tools engine's own halt
// switch mirroring the CLI driver's terminal-status handling (STACK-2,
// 2026-08-16, extended for the canceled status).

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
)

// TestStackHasProgressCanceledChunkHalts pins that a canceled chunk task
// halts the drive (stackHasProgress returns false) exactly like a failed
// one: it exists only because a dependency died, so nothing here can move
// the stack forward on its own.
func TestStackHasProgressCanceledChunkHalts(t *testing.T) {
	byID := map[string]ledger.Task{
		"c1": {ID: "c1", Status: stacking.StatusCanceled},
	}
	if stackHasProgress(byID, true) {
		t.Fatal("stackHasProgress() = true with a canceled chunk, want false (halt)")
	}
	if stackHasProgress(byID, false) {
		t.Fatal("stackHasProgress() = true with a canceled chunk and no merge oracle, want false (halt)")
	}
}
