package coordinator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// claimStealingRepo steals the run claim for a rival holder immediately
// before the coordinator's own first ClaimRun, reproducing deterministically
// the verify-main-race CI interleaving: a concurrent joiner claims the run
// inside the window between AdmitSingleTask and the fresh-admission
// winner's ClaimRun at ensure.go.
type claimStealingRepo struct {
	ledger.LedgerRepository
	mu    sync.Mutex
	stole bool
}

const rivalHolder = "rival-holder"

func (r *claimStealingRepo) ClaimRun(ctx context.Context, runID, holder string) error {
	r.mu.Lock()
	steal := !r.stole
	r.stole = true
	r.mu.Unlock()
	if steal {
		if err := r.LedgerRepository.ClaimRun(ctx, runID, rivalHolder); err != nil {
			return err
		}
	}
	return r.LedgerRepository.ClaimRun(ctx, runID, holder)
}

// TestSingleTaskAdmissionWinnerJoinsWhenClaimLost pins the fix for the
// verify-main-race failure ("ledger: run claim held by another executor"):
// the fresh-admission winner whose claim raced away must JOIN the run it
// just durably admitted - execution belongs to the claim holder - not fail
// the caller with the raw claim error. Once the rival releases, the joined
// handle's reclaim watcher resumes and completes the task itself.
func TestSingleTaskAdmissionWinnerJoinsWhenClaimLost(t *testing.T) {
	inner := ledger.NewMemoryLedgerRepository()
	repo := &claimStealingRepo{LedgerRepository: inner}
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, subagents.HandlerOneshot, invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1})).(*coordinator)

	req := EnsureRunRequest{
		RunID:          NewRunID(),
		Tasks:          []subagents.Task{{ID: "t1", Name: subagents.HandlerOneshot, Input: json.RawMessage(`"work"`)}},
		IdempotencyKey: "claim-lost-admission",
	}
	h, err := c.EnsureSingleTaskRun(context.Background(), req)
	if err != nil {
		t.Fatalf("EnsureSingleTaskRun with a raced-away claim = %v, want a joined handle", err)
	}
	if h == nil {
		t.Fatal("EnsureSingleTaskRun returned a nil handle")
	}

	// The rival abandons the run; the joined handle's reclaim watcher (500ms
	// cadence) must take over, resume, and complete the admitted task.
	if err := inner.ReleaseRun(context.Background(), req.RunID, rivalHolder); err != nil {
		t.Fatalf("release rival claim: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := h.owner.Join(ctx, h)
	if err != nil {
		t.Fatalf("join after rival release: %v", err)
	}
	if result == nil {
		t.Fatal("nil run result")
	}
}
