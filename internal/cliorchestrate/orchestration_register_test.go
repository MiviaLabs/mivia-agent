package cliorchestrate

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// spawnRegisteredRunHandle builds a real coordinator plus a real child run
// handle, the shape the workflow host wiring hands the seam. The returned
// cleanup deletes the registry entry the caller creates for the handle.
func spawnRegisteredRunHandle(t *testing.T) (coord coordinator.Coordinator, h *coordinator.RunHandle, d *runtime.Dispatcher, repo ledger.LedgerRepository) {
	t.Helper()
	repo = ledger.NewMemoryLedgerRepository()
	coord = coordinator.New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	// One unnamed task: no dispatcher handler matches it, so the child settles
	// fast without touching a provider. The handle identity is what the seam
	// needs, not a live task.
	var err error
	h, err = coord.Spawn(context.Background(), []subagents.Task{{ID: "child-1"}}, "register-seam-child")
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	d = runtime.New(runtime.Policy{})
	return coord, h, d, repo
}

// TestRegisterChildRunHandleAccessMatrix pins who may resolve a registered
// workflow child run through the accessible-handle path:
//   - the owning session resolves it;
//   - a foreign session gets THE ONE unknown envelope (INV-AG-9: unknown and
//     inaccessible are indistinguishable - one literal, no branch);
//   - a refused (zero-owner) registration leaves nothing to resolve.
func TestRegisterChildRunHandleAccessMatrix(t *testing.T) {
	coord, h, d, repo := spawnRegisteredRunHandle(t)
	runID := h.RunID()
	t.Cleanup(func() { runHandles.Delete(runID) })
	// Default retention (10m): the matrix pins resolution, not eviction, and
	// must not depend on how fast the child settles.
	cfg := config.SubagentConfig{}

	if err := RegisterChildRunHandle(runID, coord, h, repo, d, "sess-owner", cfg); err != nil {
		t.Fatalf("RegisterChildRunHandle() error = %v", err)
	}

	cases := []struct {
		name      string
		sessionID string
		wantJSON  string
	}{
		{"owning session resolves the child run", "sess-owner", ""},
		{"foreign session gets the one unknown envelope", "sess-other", errJSONUnknownRunID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: tc.sessionID})
			record, errJSON := AccessibleOrchestrationHandle(ctx, runID, d, repo)
			if errJSON != tc.wantJSON {
				t.Fatalf("AccessibleOrchestrationHandle() errJSON = %q, want %q", errJSON, tc.wantJSON)
			}
			if tc.wantJSON == "" {
				if record == nil {
					t.Fatal("AccessibleOrchestrationHandle() record = nil, want the child run record")
				}
				if got := record.GetHandle().RunID(); got != runID {
					t.Fatalf("resolved run ID = %q, want %q", got, runID)
				}
			}
		})
	}

	// Zero-owner registration is refused before any record exists, so there is
	// nothing to resolve: the tools see the same unknown envelope.
	if err := RegisterChildRunHandle(runID, coord, h, repo, d, "", cfg); err == nil {
		t.Fatal("RegisterChildRunHandle() with an empty owner = nil error, want refusal")
	}
	if _, ok := runHandles.Load("unregistered-" + runID); ok {
		t.Fatal("a refused registration stored a record")
	}
}

// TestRegisterChildRunHandleKeepsOriginalOwner pins the LoadOrStore rule: a
// repeat registration for an existing run ID (the resume re-ensure path) must
// keep the ORIGINAL owner, not hand the run to the resuming session.
func TestRegisterChildRunHandleKeepsOriginalOwner(t *testing.T) {
	coord, h, d, repo := spawnRegisteredRunHandle(t)
	runID := h.RunID()
	t.Cleanup(func() { runHandles.Delete(runID) })
	cfg := config.SubagentConfig{}

	if err := RegisterChildRunHandle(runID, coord, h, repo, d, "sess-original", cfg); err != nil {
		t.Fatalf("RegisterChildRunHandle() error = %v", err)
	}
	if err := RegisterChildRunHandle(runID, coord, h, repo, d, "sess-resuming", cfg); err != nil {
		t.Fatalf("repeat RegisterChildRunHandle() error = %v", err)
	}
	raw, ok := runHandles.Load(runID)
	if !ok {
		t.Fatal("registered run record is missing")
	}
	if got := PrincipalSessionIDOfHandle(raw.(*OrchestrationHandleForTest)); got != "sess-original" {
		t.Fatalf("principal session = %q, want the original owner %q", got, "sess-original")
	}
}

// TestRegisterChildRunHandleRefusesNilDispatcherAndHandle pins the boundary
// assertions: a nil dispatcher would panic in the OnClose closure, and a nil
// handle would panic in the retention goroutine. Both must be refused before
// any registry write.
func TestRegisterChildRunHandleRefusesNilDispatcherAndHandle(t *testing.T) {
	coord, h, d, repo := spawnRegisteredRunHandle(t)
	runID := h.RunID()
	t.Cleanup(func() { runHandles.Delete(runID) })
	cfg := config.SubagentConfig{}

	if err := RegisterChildRunHandle(runID, coord, h, repo, nil, "sess-owner", cfg); err == nil {
		t.Fatal("RegisterChildRunHandle() with a nil dispatcher = nil error, want refusal")
	}
	if err := RegisterChildRunHandle(runID, coord, nil, repo, d, "sess-owner", cfg); err == nil {
		t.Fatal("RegisterChildRunHandle() with a nil handle = nil error, want refusal")
	}
	if _, ok := runHandles.Load(runID); ok {
		t.Fatal("a refused registration stored a record")
	}
}

// TestRegisterChildRunHandleOnCloseEviction is the eviction regression: a
// completed child run is evicted when its dispatcher closes (the OnClose path
// in storeOrchestrationHandle), and an evicted child run answers with the one
// unknown envelope again.
func TestRegisterChildRunHandleOnCloseEviction(t *testing.T) {
	coord, h, d, repo := spawnRegisteredRunHandle(t)
	runID := h.RunID()
	cfg := config.SubagentConfig{HandleRetentionSeconds: 1}

	if err := RegisterChildRunHandle(runID, coord, h, repo, d, "sess-owner", cfg); err != nil {
		t.Fatalf("RegisterChildRunHandle() error = %v", err)
	}
	// Complete the child run so only the OnClose eviction can remove it.
	if err := coord.Cancel(context.Background(), h); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("child run did not settle after cancel")
	}

	d.Close()

	if _, ok := runHandles.Load(runID); ok {
		t.Fatal("completed child run survived dispatcher close; OnClose eviction did not run")
	}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-owner"})
	if _, errJSON := AccessibleOrchestrationHandle(ctx, runID, d, repo); errJSON != errJSONUnknownRunID {
		t.Fatalf("evicted child run errJSON = %q, want %q", errJSON, errJSONUnknownRunID)
	}
}
