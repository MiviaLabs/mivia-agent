package ledger

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// panelCancelHandlerFunc adapts a plain function to runtime.Handler.
type panelCancelHandlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f panelCancelHandlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}

// panelCancelFixture builds a real PanelCoordinator over a real coordinator
// and an admitted 2-member panel attempt in members_admitted phase, ready
// for a test to drive toward cancel_pending.
func panelCancelFixture(t *testing.T, handler runtime.Handler) (PanelCoordinator, *StorageRepository, coordinator.Coordinator, string, StepAttempt) {
	t.Helper()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "agent", handler); err != nil {
		t.Fatal(err)
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	coord := coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))

	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	panel := NewPanelCoordinator(run, coord, repo)
	return panel, repo, coord, run, stored
}

func claimAndCancelPending(t *testing.T, repo *StorageRepository, run string, attempt StepAttempt) context.Context {
	t.Helper()
	ctx := context.Background()
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	claimCtx := ContextWithClaimHolder(ctx, "holder")
	if err := repo.CompareAndSetPanelPhase(claimCtx, run, attempt.AttemptID, attempt.Version, PanelPhaseMembersAdmitted, PanelPhaseCancelPending, nil); err != nil {
		t.Fatal(err)
	}
	return claimCtx
}

func TestCancelOrTombstoneMember_TombstonesNeverDispatchedMember(t *testing.T) {
	invoked := 0
	handler := panelCancelHandlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		invoked++
		return json.RawMessage(`{}`), nil
	})
	panel, repo, coord, run, attempt := panelCancelFixture(t, handler)
	claimCtx := claimAndCancelPending(t, repo, run, attempt)

	terminal, err := panel.CancelOrTombstoneMember(claimCtx, attempt.AttemptID, "member-0")
	if err != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, err)
	}

	// A stale forward caller must join the tombstone, not dispatch a handler.
	member := attempt.PanelExecution.Members[0]
	req, err := panel.request(claimCtx, member.CoordinatorRunID, member.TaskID, member.Work, true)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := coord.EnsureSingleTaskRun(ContextWithPanelChildPrincipal(claimCtx, run), req)
	if err != nil {
		t.Fatalf("stale EnsureSingleTaskRun error = %v", err)
	}
	result, err := coord.Join(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != string(coordledger.TaskStatusCanceled) {
		t.Fatalf("stale join result = %+v", result)
	}
	if invoked != 0 {
		t.Fatalf("handler invoked %d times, want 0", invoked)
	}

	// Idempotent re-entry: canceling an already-tombstoned member again is a no-op.
	terminal, err = panel.CancelOrTombstoneMember(claimCtx, attempt.AttemptID, "member-0")
	if err != nil || !terminal {
		t.Fatalf("re-entry terminal=%v err=%v", terminal, err)
	}
}

func TestCancelOrTombstoneMember_RequiresCancelPendingPhase(t *testing.T) {
	handler := panelCancelHandlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	panel, repo, _, run, attempt := panelCancelFixture(t, handler)
	ctx := context.Background()
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	claimCtx := ContextWithClaimHolder(ctx, "holder")

	if _, err := panel.CancelOrTombstoneMember(claimCtx, attempt.AttemptID, "member-0"); err == nil {
		t.Fatal("cancel before the phase reaches cancel_pending must fail")
	}
}

func TestCancelOrTombstoneSynthesis_NoIntentIsAlreadyTerminal(t *testing.T) {
	handler := panelCancelHandlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	panel, repo, _, run, attempt := panelCancelFixture(t, handler)
	claimCtx := claimAndCancelPending(t, repo, run, attempt)

	terminal, err := panel.CancelOrTombstoneSynthesis(claimCtx, attempt.AttemptID)
	if err != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, err)
	}
}

func TestCancelOrTombstoneMember_CancelsLiveLocalActor(t *testing.T) {
	release := make(chan struct{})
	handler := panelCancelHandlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-release:
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	panel, repo, _, run, attempt := panelCancelFixture(t, handler)
	defer close(release)

	// Dispatch member-0 for real while still in members_admitted phase.
	membersCtx := ContextWithClaimHolder(context.Background(), "holder")
	if err := repo.ClaimRun(membersCtx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	handle, err := panel.EnsureMember(membersCtx, attempt.AttemptID, "member-0")
	if err != nil {
		t.Fatalf("EnsureMember() error = %v", err)
	}
	if !handle.LocalActor() {
		t.Fatal("expected member-0 to become a local actor")
	}

	claimCtx := claimAndCancelPending(t, repo, run, attempt)
	terminal, err := panel.CancelOrTombstoneMember(claimCtx, attempt.AttemptID, "member-0")
	if err != nil || !terminal {
		t.Fatalf("terminal=%v err=%v", terminal, err)
	}
	select {
	case <-handle.Done():
	default:
		t.Fatal("canceling a live local actor must wait for it to reach a terminal state")
	}
}
