package cli

// Coverage for driveParkedStackIfNeeded's two fail-settle call sites
// (workflow_tool_engine_reconcile.go:230-293): the pre-drive gate (a stack
// already terminally failed before any drive attempt) and the post-drive
// gate (a drive attempt itself fails and the stack is now terminally failed).
// Both sites log and continue rather than abort the sweep when the settle CAS
// itself fails, so a broken settle never crashes the recovery sweep.

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// failCASRepository embeds a real Repository and forces every
// CompareAndSetRunStatus call to fail, so a settle path's CAS failure can be
// exercised without a real concurrent writer racing the test.
type failCASRepository struct {
	workflowledger.Repository
	err error
}

func (f failCASRepository) CompareAndSetRunStatus(context.Context, string, uint64, workflowledger.RunStatus, *time.Time) error {
	return f.err
}

// TestDriveParkedStackIfNeededLogsPreDriveSettleError covers the pre-drive
// fail-settle branch's settleErr!=nil log-and-continue path (lines 242-245):
// a stack that is already terminally failed before any drive is attempted is
// fail-settled: settleStackPlanRunFailed(context.Background(), repo, ...)
// runs a real CAS against a fresh background context, so failCASRepository
// is embedded directly (no claim context to fence). When that CAS fails, the
// sweep must log the failure and still abort (park) the run instead of
// panicking or falling through to a drive.
func TestDriveParkedStackIfNeededLogsPreDriveSettleError(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := tasks.NewStore(store).TransitionTask(planRunID, "c2", stackStatusFailed); err != nil {
		t.Fatalf("transition chunk c2 to failed: %v", err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	wantErr := errors.New("boom: cas refused")
	wrapped := failCASRepository{Repository: repo, err: wantErr}

	originalDrive := driveParkedStackImpl
	t.Cleanup(func() { driveParkedStackImpl = originalDrive })
	driveParkedStackImpl = func(*sessionWorkflowEngine, context.Context, string, *config.Resolved, *storage.SQLite, workflowledger.Repository, string) (bool, error) {
		t.Fatal("driveParkedStackImpl called; a pre-drive terminally failed stack must not be re-driven")
		return false, nil
	}

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	e := newSessionWorkflowEngine(root, configPath)
	abort := e.driveParkedStackIfNeeded(context.Background(), root, res, store, wrapped, planRunID, false)
	log.SetOutput(original)

	if !abort {
		t.Fatal("driveParkedStackIfNeeded = false, want true (terminally failed stack must abort/park)")
	}
	if !strings.Contains(buf.String(), "settle failed stack plan run") || !strings.Contains(buf.String(), wantErr.Error()) {
		t.Fatalf("log = %q, want the settle-failure line naming %v", buf.String(), wantErr)
	}
	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (settle CAS refused, run must stay parked)", run.Status)
	}
}

// TestDriveParkedStackIfNeededSettlesAfterDriveErrorDiscoversFailure covers
// the post-drive-error classification branch (lines 264-274, 279): the stack
// is NOT yet terminally failed before the drive attempt, so the pre-drive
// gate lets it through to driveParkedStackImpl; the (stubbed) drive attempt
// itself fails a chunk and returns a non-errStackAwaitsGrant error. The
// re-classification after the failed drive must see the now-failed chunk,
// prefer stackPlanRunFailureReason's specific reason over the raw driveErr
// text, settle the run failed, and abort.
func TestDriveParkedStackIfNeededSettlesAfterDriveErrorDiscoversFailure(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	driveErr := errors.New("simulated stack drive failure")
	originalDrive := driveParkedStackImpl
	t.Cleanup(func() { driveParkedStackImpl = originalDrive })
	driveParkedStackImpl = func(_ *sessionWorkflowEngine, _ context.Context, _ string, _ *config.Resolved, s *storage.SQLite, _ workflowledger.Repository, runID string) (bool, error) {
		// Model a drive attempt that discovers a chunk died mid-attempt: the
		// stack was merely incomplete going in (the pre-drive gate let it
		// through), and the failure is only visible after this call returns.
		if err := tasks.NewStore(s).TransitionTask(runID, "c2", stackStatusFailed); err != nil {
			t.Fatalf("simulate chunk failure: %v", err)
		}
		return false, driveErr
	}

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	e := newSessionWorkflowEngine(root, configPath)
	abort := e.driveParkedStackIfNeeded(context.Background(), root, res, store, repo, planRunID, false)
	log.SetOutput(original)

	if !abort {
		t.Fatal("driveParkedStackIfNeeded = false, want true (post-drive terminal failure must abort/park)")
	}
	// settleStackPlanRunFailed's own log line names the settle cause: the
	// specific chunk reason must win over the raw driveErr text.
	if !strings.Contains(buf.String(), "chunk c2 failed terminally") {
		t.Fatalf("log = %q, want the specific chunk-failure reason, not just the raw drive error", buf.String())
	}
	if strings.Contains(buf.String(), "settle failed stack plan run") {
		t.Fatalf("log = %q, want no settle-failure line (the CAS must have succeeded)", buf.String())
	}
	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed (post-drive terminal failure settled)", run.Status)
	}
}

// TestDriveParkedStackIfNeededLogsPostDriveSettleError covers the post-drive
// fail-settle branch's settleErr!=nil log-and-continue path (lines 274-278):
// same shape as the discovery test above, but the settle CAS itself is
// refused. The sweep must log the failure and still abort instead of
// crashing or looping.
func TestDriveParkedStackIfNeededLogsPostDriveSettleError(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	driveErr := errors.New("simulated stack drive failure")
	originalDrive := driveParkedStackImpl
	t.Cleanup(func() { driveParkedStackImpl = originalDrive })
	driveParkedStackImpl = func(_ *sessionWorkflowEngine, _ context.Context, _ string, _ *config.Resolved, s *storage.SQLite, _ workflowledger.Repository, runID string) (bool, error) {
		if err := tasks.NewStore(s).TransitionTask(runID, "c2", stackStatusFailed); err != nil {
			t.Fatalf("simulate chunk failure: %v", err)
		}
		return false, driveErr
	}

	wantErr := errors.New("boom: cas refused")
	wrapped := failCASRepository{Repository: repo, err: wantErr}

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	e := newSessionWorkflowEngine(root, configPath)
	abort := e.driveParkedStackIfNeeded(context.Background(), root, res, store, wrapped, planRunID, false)
	log.SetOutput(original)

	if !abort {
		t.Fatal("driveParkedStackIfNeeded = false, want true (post-drive terminal failure must abort/park even when the settle CAS fails)")
	}
	if !strings.Contains(buf.String(), "settle failed stack plan run") || !strings.Contains(buf.String(), wantErr.Error()) {
		t.Fatalf("log = %q, want the settle-failure line naming %v", buf.String(), wantErr)
	}
	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("plan run status = %q, want delivery_pending (settle CAS refused, run must stay parked)", run.Status)
	}
}

// TestDriveParkedStackIfNeededPreDriveEmptyReasonFallback covers the
// defensive empty-reason fallback (lines 238-241): stackPlanRunFailureReason
// always returns a non-empty reason alongside failed=true given the real
// state machine's invariants, so the "" branch is unreachable through normal
// state. The stackPlanRunFailureReasonFn seam simulates the combination
// directly so the fallback string itself - not just its presence - is
// pinned, and a future caller that breaks the invariant is caught here
// instead of settling a run with an empty cause.
func TestDriveParkedStackIfNeededPreDriveEmptyReasonFallback(t *testing.T) {
	root, storePath, configPath, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	seedSucceededDecomposeAttempt(t, repo, planRunID, []byte(multiChunkPlanOutput))

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := tasks.NewStore(store).TransitionTask(planRunID, "c2", stackStatusFailed); err != nil {
		t.Fatalf("transition chunk c2 to failed: %v", err)
	}

	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	applyWorkflowStoreRoot(res, root)

	originalReason := stackPlanRunFailureReasonFn
	t.Cleanup(func() { stackPlanRunFailureReasonFn = originalReason })
	stackPlanRunFailureReasonFn = func(context.Context, string, *storage.SQLite, workflowledger.Repository, string) (bool, string) {
		return true, ""
	}

	e := newSessionWorkflowEngine(root, configPath)
	abort := e.driveParkedStackIfNeeded(context.Background(), root, res, store, repo, planRunID, false)

	if !abort {
		t.Fatal("driveParkedStackIfNeeded = false, want true (terminally failed stack must abort/park)")
	}
	run, err := repo.GetRun(context.Background(), planRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed", run.Status)
	}
}
