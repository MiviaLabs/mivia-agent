package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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
	// The pool routes by task.Name (validPanelTask sets TaskName to the
	// member/synthesis ID), not AgentName, so every name a fixture's panel
	// attempt can dispatch must resolve to the same handler.
	for _, name := range []string{"member-0", "member-1", "synthesis"} {
		if err := dispatcher.Register(runtime.Subagent, name, handler); err != nil {
			t.Fatal(err)
		}
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

// Cancellation matrix item 5: a slow worker produces cancel_pending
// (terminal=false, err=nil), not an ambiguous-claim refusal (item 6,
// terminal=false with a non-nil error). A caller-context deadline while
// waiting for an uncooperative-but-live child to finish is the slow-worker
// signal, distinct from a claim the coordinator cannot safely verify at all.
func TestCancelOrTombstoneMember_SlowWorkerReportsPendingNotBlocked(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	defer close(release)
	// Deliberately does not observe ctx.Done(): simulates a worker that is
	// genuinely still running and has not yet reacted to cancellation, so
	// only the caller's own deadline elapses, not the child's completion.
	// The coordinator CASes the task to running before dispatching it to the
	// pool (dag.go's startReady), so closing started as the handler's first
	// action is a deterministic signal that the durable status is already
	// "running" by the time this test proceeds.
	var startedOnce sync.Once
	handler := panelCancelHandlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		startedOnce.Do(func() { close(started) })
		<-release
		return json.RawMessage(`{}`), nil
	})
	panel, repo, _, run, attempt := panelCancelFixture(t, handler)

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
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("member-0's handler never started")
	}

	claimCtx := claimAndCancelPending(t, repo, run, attempt)
	shortCtx, cancel := context.WithTimeout(claimCtx, 20*time.Millisecond)
	defer cancel()
	terminal, err := panel.CancelOrTombstoneMember(shortCtx, attempt.AttemptID, "member-0")
	if err != nil {
		t.Fatalf("slow worker must report cancel_pending (nil error), got err = %v", err)
	}
	if terminal {
		t.Fatal("a worker that has not yet stopped must not be reported terminal")
	}
}

// joinAsRecoveredRaceCoordinator wraps a real coordinator.Coordinator and, the
// first time its JoinAsRecovered observes ledger.ErrNotFound (member never
// admitted yet), synchronously runs a caller-supplied hook before returning.
// This deterministically reproduces the exact race window inside
// PanelCoordinator.cancelOrTombstone between its own JoinAsRecovered call
// (ErrNotFound) and its subsequent EnsureTerminalSingleTaskRun call: the hook
// lets a *different* coordinator instance (a concurrent forward dispatcher)
// win admission for the same idempotency key in that gap, every run, with no
// goroutines or timing luck required.
type joinAsRecoveredRaceCoordinator struct {
	coordinator.Coordinator
	once sync.Once
	hook func()
}

func (r *joinAsRecoveredRaceCoordinator) JoinAsRecovered(ctx context.Context, req coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	h, err := r.Coordinator.JoinAsRecovered(ctx, req)
	if errors.Is(err, coordledger.ErrNotFound) {
		r.once.Do(r.hook)
	}
	return h, err
}

// TestCancelOrTombstoneMember_ConcurrentAdmissionWinnerIsNotFalselyTerminal is
// a regression test for the bug where cancelOrTombstone discarded the handle
// returned by its own EnsureTerminalSingleTaskRun call and unconditionally
// reported (terminal=true, err=nil). When a concurrent forward dispatcher
// wins the underlying admission race for a never-admitted child in the gap
// between this method's JoinAsRecovered (ErrNotFound) and EnsureTerminal-
// SingleTaskRun calls, the latter returns a live, non-terminal join handle
// onto the winner's run with a nil error (coordinator's wait-only
// joinSingleTaskAdmission ErrClaimHeld branch) instead of admitting our own
// tombstone. The fix must route that handle through the same Cancel call as
// any other found handle, which fails closed (ErrClaimHeld) because a
// different coordinator instance holds the run's claim - not silently report
// the still-running member as canceled.
// concurrentAdmissionRaceFixture builds two independent coordinator
// instances sharing one durable ledger (a stale forward dispatcher and the
// canceler), plus an admitted 2-member panel attempt, for tests that need to
// race a concurrent forward admission against a cancel call.
func concurrentAdmissionRaceFixture(t *testing.T, handler runtime.Handler) (forwardDispatcher, cancelerInner coordinator.Coordinator, repo *StorageRepository, run string, attempt StepAttempt) {
	t.Helper()
	dispatcher := runtime.New(runtime.Policy{})
	for _, name := range []string{"member-0", "member-1", "synthesis"} {
		if err := dispatcher.Register(runtime.Subagent, name, handler); err != nil {
			t.Fatal(err)
		}
	}
	coordLedger := coordledger.NewMemoryLedgerRepository()
	// Two independent coordinator instances over the SAME durable ledger,
	// each with its own random holder ID - exactly what a stale forward
	// dispatcher vs. this cancel path would be after a claim-lease handoff.
	forwardDispatcher = coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))
	cancelerInner = coordinator.New(coordLedger, subagents.New(dispatcher, subagents.Policy{Workers: 4}))

	repo = newMemoryRepo(t)
	ctx := context.Background()
	run = runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt = StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	attempt, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	return forwardDispatcher, cancelerInner, repo, run, attempt
}

func TestCancelOrTombstoneMember_ConcurrentAdmissionWinnerIsNotFalselyTerminal(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	handler := panelCancelHandlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		select {
		case <-release:
			return json.RawMessage(`{}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	forwardDispatcher, cancelerInner, repo, run, attempt := concurrentAdmissionRaceFixture(t, handler)

	raceCoord := &joinAsRecoveredRaceCoordinator{Coordinator: cancelerInner}
	panel := NewPanelCoordinator(run, raceCoord, repo)

	member := attempt.PanelExecution.Members[0]
	req, err := panel.request(context.Background(), member.CoordinatorRunID, member.TaskID, member.Work, true)
	if err != nil {
		t.Fatal(err)
	}
	var winnerHandle *coordinator.RunHandle
	raceCoord.hook = func() {
		// Never-admitted member: a concurrent forward dispatcher wins the
		// admission race for the same idempotency key right after this
		// method observed ErrNotFound.
		h, err := forwardDispatcher.EnsureSingleTaskRun(ContextWithPanelChildPrincipal(context.Background(), run), req)
		if err != nil {
			t.Fatalf("concurrent forward dispatch error = %v", err)
		}
		if !h.LocalActor() {
			t.Fatal("forward dispatcher must become member-0's local actor")
		}
		winnerHandle = h
	}

	claimCtx := claimAndCancelPending(t, repo, run, attempt)
	terminal, err := panel.CancelOrTombstoneMember(claimCtx, attempt.AttemptID, "member-0")
	if err == nil {
		t.Fatalf("CancelOrTombstoneMember must fail closed when a concurrent dispatcher wins admission, got terminal=%v err=nil", terminal)
	}
	if terminal {
		t.Fatalf("CancelOrTombstoneMember must never report terminal=true while the concurrently admitted member is still live, got err = %v", err)
	}
	if winnerHandle == nil {
		t.Fatal("race hook never ran; test setup did not exercise the ErrNotFound path")
	}
	select {
	case <-winnerHandle.Done():
		t.Fatal("the concurrently admitted member finished before the assertions ran; test no longer proves it was live")
	default:
	}
}
