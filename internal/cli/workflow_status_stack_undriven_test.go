package cli

// Pins the self-diagnosing surface added alongside classifyStackPlanRunDelivery
// (see workflow_deliver_stack_refuse_test.go): `mivia workflow status` on a
// plan run parked at delivery_pending with an undriven multi-chunk stack must
// print an explicit "stack: UNDRIVEN" notice instead of looking identical to
// a normal, healthy pending delivery - and, per F11, must NOT print that
// notice for a stack that already drove to completion, since `mivia workflow
// deliver` no longer refuses that run.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestWorkflowStatusFlagsUndrivenStackPlanRun(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	ctx := context.Background()
	runID := "wfr-status-undriven"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))

	// The run's actual stored status is irrelevant to the notice - it takes
	// the caller's status directly (executeWorkflowStatus passes the freshly
	// read run.Status), so a settled delivery_pending is simulated here
	// without needing a full controller transition.

	var buf bytes.Buffer
	printStackUndrivenNotice(ctx, &buf, root, store, repo, runID, workflowledger.RunStatusDeliveryPending)
	out := buf.String()
	if !strings.Contains(out, "UNDRIVEN") || !strings.Contains(out, "stack drive") {
		t.Fatalf("stack-undriven notice = %q, want it to flag UNDRIVEN and point at `mivia stack drive`", out)
	}
	// The notice must not advise `mivia workflow run`/`mivia workflow
	// resume`: resume refuses delivery_pending runs and run mints a NEW
	// plan run, so both are dead ends for a parked stack.
	if strings.Contains(out, "mivia workflow run") || strings.Contains(out, "mivia workflow resume") {
		t.Fatalf("stack-undriven notice = %q, must not point at workflow run/resume dead ends", out)
	}
}

// TestWorkflowStatusNoticeOmittedElsewhere pins the fail-open shape: a run
// that is not delivery_pending, or one that is not a multi-chunk plan run,
// must never print the notice.
func TestWorkflowStatusNoticeOmittedElsewhere(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	ctx := context.Background()

	// A multi-chunk plan run that hasn't reached delivery_pending yet - the
	// notice must short-circuit on status alone, before it ever looks at the
	// decompose attempt.
	runningID := "wfr-status-running"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runningID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runningID, []byte(multiChunkPlanOutput))
	var buf1 bytes.Buffer
	printStackUndrivenNotice(ctx, &buf1, root, store, repo, runningID, workflowledger.RunStatusRunning)
	if buf1.Len() != 0 {
		t.Fatalf("running run must not print the undriven notice, got %q", buf1.String())
	}

	// A delivery_pending run with no decompose attempt at all (a chunk run,
	// or a non-stacking workflow).
	chunkID := "wfr-status-chunk"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: chunkID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	printStackUndrivenNotice(ctx, &buf2, root, store, repo, chunkID, workflowledger.RunStatusDeliveryPending)
	if buf2.Len() != 0 {
		t.Fatalf("a run with no decompose attempt must not print the undriven notice, got %q", buf2.String())
	}
}

// TestWorkflowStatusNoticeOmittedForCompletedStack pins F11: a multi-chunk
// plan run whose stack already drove to completion (every chunk merged,
// integration run settled) must NOT print the UNDRIVEN notice, even while
// parked at delivery_pending - `mivia workflow deliver` settles this run
// now, it does not refuse it, so telling the operator it "refuses this run"
// would be actively wrong.
func TestWorkflowStatusNoticeOmittedForCompletedStack(t *testing.T) {
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	var buf bytes.Buffer
	printStackUndrivenNotice(context.Background(), &buf, root, store, repo, stackID, workflowledger.RunStatusDeliveryPending)
	if buf.Len() != 0 {
		t.Fatalf("a driven-to-completion stack must not print the undriven notice, got %q", buf.String())
	}
}

// countingProbeGit counts every git call a test-substituted runner serves.
type countingProbeGit struct{ runs int }

func (g *countingProbeGit) Run(context.Context, delivery.GitContext, ...string) (string, error) {
	g.runs++
	return "", errors.New("test: probe must not run")
}

// countingProbePR counts IsMerged calls on a test-substituted PR client.
type countingProbePR struct{ merged int }

func (c *countingProbePR) FindByHead(context.Context, string, string) (*delivery.PRRef, error) {
	return nil, nil
}
func (c *countingProbePR) Create(context.Context, string, delivery.PRInput) (delivery.PRRef, error) {
	return delivery.PRRef{}, nil
}
func (c *countingProbePR) IsMerged(context.Context, string, string) (bool, error) {
	c.merged++
	return false, nil
}

// TestWorkflowStatusNoticeSkipsRemoteMergeOracle pins that the status
// notice path never reaches the remote merge oracle: workflow status is a
// read-only command and must not run git or gh network probes. For an
// auto-policy stack whose integration run is succeeded with durable pushed
// evidence, the notice must take its verdict from that durable (local)
// evidence alone - and print nothing - even though the settle paths
// (deliver, stack drive, sweep) would still ask the oracle whether the PR
// actually merged before settling the plan run.
func TestWorkflowStatusNoticeSkipsRemoteMergeOracle(t *testing.T) {
	root, storePath, _, _ := newDeliveryFixture(t)
	repo := openDeliveryStore(t, storePath)
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	planRunID := seedParkedStackingPlanRun(t, root, storePath, repo)
	mergeParkedStackChunks(t, storePath, repo, planRunID)

	// Integration run: settled succeeded over durable pushed evidence, with
	// a head branch and remote the merge oracle would probe.
	intRun := seedIntegrationRunAdmitted(t, repo, planRunID, false)
	stored, err := repo.GetRun(ctx, intRun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, intRun.RunID, stored.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID: intRun.RunID, IdempotencyKey: "status-notice", Mode: "draft",
		BaseRef: "main", HeadRef: "wf/wt-integration", CommitSHA: "abc123",
		Status: "pushed", UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	git := &countingProbeGit{}
	pr := &countingProbePR{}
	prevGit, prevNewPR := workflowDeliverGit, workflowDeliverNewPR
	workflowDeliverGit = git
	workflowDeliverNewPR = func() delivery.PRClient { return pr }
	t.Cleanup(func() {
		workflowDeliverGit = prevGit
		workflowDeliverNewPR = prevNewPR
	})

	var buf bytes.Buffer
	printStackUndrivenNotice(ctx, &buf, root, store, repo, planRunID, workflowledger.RunStatusDeliveryPending)
	if buf.Len() != 0 {
		t.Fatalf("durable pushed evidence must read complete for display; notice = %q", buf.String())
	}
	if git.runs != 0 || pr.merged != 0 {
		t.Fatalf("status notice ran remote probes: git calls = %d, IsMerged calls = %d; want 0/0", git.runs, pr.merged)
	}
}
