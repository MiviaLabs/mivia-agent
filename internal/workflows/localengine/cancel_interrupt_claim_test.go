package localengine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// blockingGit blocks the FIRST git call (a hung publish) until release or ctx
// cancel, then delegates every later call to after. It models a live delivery
// mid-publish whose git runner is wedged.
type blockingGit struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	after   func(ctx context.Context, gc delivery.GitContext, args ...string) (string, error)
}

type cancelClaimCountRepo struct {
	workflowledger.Repository
	claimCalls int
}

func (r *cancelClaimCountRepo) ClaimRun(ctx context.Context, runID, holder string) error {
	r.claimCalls++
	return r.Repository.ClaimRun(ctx, runID, holder)
}

func (g *blockingGit) Run(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if g.after != nil {
		return g.after(ctx, gc, args...)
	}
	return "", fmt.Errorf("blockingGit: no delegate configured")
}

// newBlockedDeliveryEngine builds a delivery_pending run whose publish blocks
// on the first git call (a hung git runner). It returns the engine, the run,
// the git release channel, and a channel closed when Deliver returns.
func newBlockedDeliveryEngine(t *testing.T) (*localengine.Engine, workflowledger.RunSnapshot, chan struct{}, chan struct{}) {
	t.Helper()
	repoRoot, _, run, repo := newSeededDeliveryFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	git := &blockingGit{
		entered: entered,
		release: release,
		after: func(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
			return delivery.RealGit{}.Run(ctx, gc, args...)
		},
	}
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		Git:           git,
		PR:            &recordingPR{},
	}
	deliverDone := make(chan struct{})
	go func() {
		defer close(deliverDone)
		_, _ = engine.Deliver(context.Background(), run.RunID, true)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not reach its first git call")
	}
	return engine, run, release, deliverDone
}

// assertClaimHeld probes that the run claim is currently held by another
// holder (non-mutating: a fresh held claim returns ErrClaimHeld, a free claim
// returns ErrClaimNotHeld, neither writes).
func assertClaimHeld(t *testing.T, repo workflowledger.Repository, runID, when string) {
	t.Helper()
	if err := repo.TakeoverExpiredRunClaim(context.Background(), runID, "claim-probe", time.Hour); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("run claim %s = %v, want ErrClaimHeld (held by the delivery)", when, err)
	}
}

// claimNowFree asserts the run claim is released, acquiring and releasing a
// probe claim so a later assertion sees a clean slate.
func claimNowFree(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	if err := repo.ClaimRun(context.Background(), runID, "claim-probe"); err != nil {
		t.Fatalf("run claim after delivery = %v, want released", err)
	}
	_ = repo.ReleaseRun(context.Background(), runID, "claim-probe")
}

// TestEngineCancelDoesNotClearLiveDeliveryClaim: a cancel issued while THIS
// engine is mid-publish must be refused without touching the delivery claim.
// Regression: Engine.Cancel cleared ANY run claim unconditionally, so
// cancelTool could strip a live delivery claim (held by this host or another
// host mid-publish) and enable double-publish.
func TestEngineCancelDoesNotClearLiveDeliveryClaim(t *testing.T) {
	engine, run, release, deliverDone := newBlockedDeliveryEngine(t)
	assertClaimHeld(t, engine.Repo, run.RunID, "before Cancel")

	_, err := engine.Cancel(context.Background(), run.RunID)
	if err == nil || !strings.Contains(err.Error(), "cancel refused") {
		t.Fatalf("Cancel during delivery = %v, want 'cancel refused'", err)
	}
	// The live delivery claim must survive Cancel untouched.
	assertClaimHeld(t, engine.Repo, run.RunID, "after Cancel")
	fresh, getErr := engine.Repo.GetRun(context.Background(), run.RunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status after Cancel = %q, want unchanged delivery_pending", fresh.Status)
	}

	close(release)
	select {
	case <-deliverDone:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery did not finish after unblock")
	}
	// The delivery goroutine released its claim on exit.
	claimNowFree(t, engine.Repo, run.RunID)
}

func TestEngineCancelKeepsClaimThroughSettlement(t *testing.T) {
	ctx := context.Background()
	base := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = base.Close() })
	const runID = "wfr-cancel-claim-handoff"
	if err := base.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	run, err := base.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.CompareAndSetRunStatus(ctx, runID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	repo := &cancelClaimCountRepo{Repository: base}
	engine := &localengine.Engine{Repo: repo}
	if _, err := engine.Cancel(ctx, runID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if repo.claimCalls != 1 {
		t.Fatalf("ClaimRun calls = %d, want 1; cancel must not release then reclaim", repo.claimCalls)
	}
}

// TestEngineCancelRefusesFreshForeignClaim: a fresh claim held by another host
// must survive Cancel - the run is not settled and the claim is not cleared.
// Regression: Engine.Cancel deleted the claim row regardless of holder, so a
// fresh foreign claim (a live executor on another host) was stripped and the
// run could be double-settled.
func TestEngineCancelRefusesFreshForeignClaim(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	run := createDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{
		RunID: "wfr-foreign-claim", WorkflowName: "deliver-me", WorkflowDigest: "digest",
		ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
	})
	engine := &localengine.Engine{Repo: repo}
	if err := repo.ClaimRun(context.Background(), run.RunID, "foreign-host"); err != nil {
		t.Fatal(err)
	}

	_, err := engine.Cancel(context.Background(), run.RunID)
	if err == nil || !strings.Contains(err.Error(), "cancel refused") {
		t.Fatalf("Cancel with a fresh foreign claim = %v, want refusal", err)
	}
	assertClaimHeld(t, repo, run.RunID, "after refused Cancel")
	fresh, getErr := repo.GetRun(context.Background(), run.RunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status after refused Cancel = %q, want delivery_pending unchanged", fresh.Status)
	}
}

// TestEngineInterruptDoesNotClearLiveDeliveryClaim: Interrupt while THIS
// engine is mid-publish must leave the delivery claim alone so the publish is
// not stripped to a second publisher. Regression: Engine.Interrupt cleared the
// claim unconditionally, so for a delivery_pending run the exclusion fence
// vanished and another host could double-publish while this delivery kept
// publishing.
func TestEngineInterruptDoesNotClearLiveDeliveryClaim(t *testing.T) {
	engine, run, release, deliverDone := newBlockedDeliveryEngine(t)
	assertClaimHeld(t, engine.Repo, run.RunID, "before Interrupt")

	if err := engine.Interrupt(run.RunID); err != nil {
		t.Fatalf("Interrupt during delivery: %v", err)
	}
	assertClaimHeld(t, engine.Repo, run.RunID, "after Interrupt")
	fresh, getErr := engine.Repo.GetRun(context.Background(), run.RunID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status after Interrupt = %q, want delivery_pending unchanged", fresh.Status)
	}

	// The delivery goroutine keeps publishing and finishes once the block
	// lifts; it must release its own claim, never have it stripped.
	close(release)
	select {
	case <-deliverDone:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery did not finish after unblock")
	}
	claimNowFree(t, engine.Repo, run.RunID)
}
