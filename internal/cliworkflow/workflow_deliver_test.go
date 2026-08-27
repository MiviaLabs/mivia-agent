package cliworkflow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// recordingPRClient records PR boundary calls instead of invoking gh.
type recordingPRClient struct {
	mu      sync.Mutex
	creates int
	finds   int
	drafts  int
}

func (r *recordingPRClient) FindByHead(ctx context.Context, repo, headBranch string) (*delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finds++
	return nil, nil
}

func (r *recordingPRClient) Create(ctx context.Context, repo string, in delivery.PRInput) (delivery.PRRef, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	if in.Draft {
		r.drafts++
	}
	return delivery.PRRef{RemoteID: "1", URL: "https://github.com/o/r/pull/1"}, nil
}

func (r *recordingPRClient) IsMerged(context.Context, string, string) (bool, error) {
	return false, nil
}

func (r *recordingPRClient) calls() (creates, finds int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates, r.finds
}

// draftCreates reports how many Create calls carried the draft flag.
func (r *recordingPRClient) draftCreates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drafts
}

// appendWorkflowDeliveryPolicy adds a pull_request delivery policy to the
// two-step fixture workflow.
func appendWorkflowDeliveryPolicy(t *testing.T, root, mode string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "workflows", "two-step.toml")
	body := "\n[delivery]\nkind = \"pull_request\"\nmode = \"" + mode + "\"\nprovider = \"github\"\nbase = \"main\"\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// initWorkflowGitRepoWithOrigin commits the fixture files and wires a bare
// origin remote holding the base commit, so delivery admission can resolve
// the origin URL and verify the base.
func initWorkflowGitRepoWithOrigin(t *testing.T, root string) {
	t.Helper()
	initWorkflowGitRepo(t, root)
	origin := filepath.Join(root, "origin.git")
	for _, args := range [][]string{
		{"init", "--bare", origin},
		{"-C", root, "remote", "add", "origin", origin},
		{"-C", root, "push", "-u", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestExecuteWorkflowRunAllowPublishFlagParsing(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	for _, flag := range []string{"--allow-publish", "--allow-publish=true", "--allow-publish=false"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			storePath := filepath.Join(root, "workflow.db")
			t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
			writeWorkflowRunFixture(t, root, server.URL, storePath)
			var stdout strings.Builder
			args := []string{"run", "two-step", flag, "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}
			if err := RunWorkflowWithIO(args, &stdout, io.Discard); err != nil {
				t.Fatalf("RunWorkflowWithIO(%q) error = %v", args, err)
			}
			if !strings.Contains(stdout.String(), "status=succeeded") {
				t.Fatalf("stdout = %q, want status=succeeded", stdout.String())
			}
			// The base fixture has no delivery policy, so no explanation is
			// printed and the run behaves like a normal run.
			if strings.Contains(stdout.String(), "--allow-publish") {
				t.Fatalf("stdout = %q, want no delivery explanation", stdout.String())
			}
		})
	}
}

func TestExecuteWorkflowRunRefusesDeliveryWithoutWriteCapable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	appendWorkflowDeliveryPolicy(t, root, "draft")

	err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("workflow run error = %v, want a write-capability refusal mentioning the run worktree", err)
	}
}

func TestExecuteWorkflowRunDeliveryPendingExplanation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	initWorkflowGitRepoWithOrigin(t, root)

	prRecorder := &recordingPRClient{}
	originalNewPR := WorkflowDeliverNewPR
	WorkflowDeliverNewPR = func() delivery.PRClient { return prRecorder }
	t.Cleanup(func() { WorkflowDeliverNewPR = originalNewPR })

	var stdout strings.Builder
	err := RunWorkflowWithIO([]string{"run", "two-step", "--workspace", root, "--config", filepath.Join(root, "config.toml"), "--input", "task=compile"}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "status=delivery_pending") {
		t.Fatalf("stdout = %q, want status=delivery_pending", stdout.String())
	}
	if !strings.Contains(stdout.String(), "requires --allow-publish") {
		t.Fatalf("stdout = %q, want an --allow-publish explanation", stdout.String())
	}
	if !strings.Contains(stdout.String(), "deliver with: mivia workflow deliver") {
		t.Fatalf("stdout = %q, want a deliver command hint", stdout.String())
	}
	creates, finds := prRecorder.calls()
	if creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero without --allow-publish", creates, finds)
	}
}

// deliveryAgreementFixture builds a run snapshot whose definition carries the
// given delivery mode (empty omits the delivery block entirely).
func deliveryAgreementFixture(t *testing.T, mode string) (workflowledger.RunSnapshot, []byte) {
	t.Helper()
	deliveryBlock := ""
	if mode != "" {
		deliveryBlock = "\n[delivery]\nkind = \"pull_request\"\nmode = \"" + mode + "\"\nprovider = \"github\"\nbase = \"main\"\n"
	}
	toml := `version = 1
name = "delivery"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
` + deliveryBlock
	wf, _, err := definition.ParseWorkflowTOML([]byte(toml), "delivery.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   []byte(toml),
		DefinitionDigest: compiled.Digest,
		Inputs:           map[string]string{"task": "x"},
		Agents:           map[string]workflowledger.AgentSnapshot{"one": {Digest: "one"}},
	}
	raw, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-delivery-agreement", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw), InputDigest: workflowledger.InputDigest(snapshot.Inputs),
	}
	return run, raw
}

func TestValidateWorkflowResumeSnapshotDeliveryAgreement(t *testing.T) {
	remarshal := func(t *testing.T, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot) (workflowledger.RunSnapshot, []byte) {
		t.Helper()
		raw, err := workflowledger.MarshalSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		run.SnapshotDigest = workflowledger.SnapshotDigest(raw)
		return run, raw
	}

	// A snapshot delivery policy matching the admitted definition is accepted.
	run, raw := deliveryAgreementFixture(t, "draft")
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Delivery = &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"}
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := ValidateWorkflowResumeSnapshot(run, raw); err != nil {
		t.Fatalf("matching delivery policy rejected: %v", err)
	}

	// A differing mode is rejected.
	snapshot.Delivery.Mode = "ready"
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := ValidateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("differing delivery mode error = %v, want a snapshot/definition mismatch", err)
	}

	// A differing provider is rejected.
	snapshot.Delivery.Mode = "draft"
	snapshot.Delivery.Provider = "gitlab"
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := ValidateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("differing delivery provider error = %v, want a snapshot/definition mismatch", err)
	}

	// A snapshot policy without any admitted definition policy is rejected.
	run, raw = deliveryAgreementFixture(t, "")
	snapshot, err = workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Delivery = &workflowledger.DeliverySnapshot{Mode: "draft", Provider: "github", Base: "main"}
	run, raw = remarshal(t, run, snapshot)
	if _, _, _, err := ValidateWorkflowResumeSnapshot(run, raw); err == nil || !strings.Contains(err.Error(), "does not match the admitted definition") {
		t.Fatalf("undeclared delivery policy error = %v, want a snapshot/definition mismatch", err)
	}
}

// gatedGitRunner blocks its first git command until release is closed, so a
// test can hold a delivery attempt in flight and observe its run claim.
type gatedGitRunner struct {
	delivery.GitRunner
	entered chan struct{}
	release chan struct{}
}

func (g gatedGitRunner) Run(ctx context.Context, _ delivery.GitContext, _ ...string) (string, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return "", errors.New("gated git released")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// TestWorkflowDeliverClaimHeartbeatKeepsLeaseFresh: a delivery attempt can run
// for WorkflowDeliveryTimeout (10m) while its claim lease lasts only
// DefaultClaimLease. The claim heartbeat must re-claim with the same
// holder while the attempt runs (DC-2): TakeoverExpiredRunClaim with a tiny
// lease must still fail mid-publish, and after the attempt ends the release
// must stop+join the heartbeat BEFORE releasing the claim, so no late tick
// re-arms the claim row (sqlite ClaimRun INSERTs when no row exists).
func TestWorkflowDeliverClaimHeartbeatKeepsLeaseFresh(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	originalGit := WorkflowDeliverGit
	originalTimeout := WorkflowDeliveryTimeout
	originalHeartbeat := workflowDeliveryClaimHeartbeat
	gate := gatedGitRunner{entered: make(chan struct{}, 1), release: make(chan struct{})}
	WorkflowDeliverGit = gate
	WorkflowDeliveryTimeout = time.Minute
	workflowDeliveryClaimHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() {
		WorkflowDeliverGit = originalGit
		WorkflowDeliveryTimeout = originalTimeout
		workflowDeliveryClaimHeartbeat = originalHeartbeat
	})

	var stdout strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, &stdout, io.Discard)
	}()

	// Wait for the attempt to be in flight (first git command gated): the
	// claim is held and the heartbeat is refreshing it.
	select {
	case <-gate.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery never reached the gated git command")
	}

	waitClaimHeldThroughHeartbeat(t, repo, runID)

	// Let the attempt finish; the release func must stop+join the heartbeat
	// BEFORE releasing the claim.
	close(gate.release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("deliver error = nil, want the gated git failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deliver did not return after the gated git release")
	}

	// After release the claim row must be gone. Poll across several heartbeat
	// intervals: a late tick that re-armed the claim (the sqlite ClaimRun
	// re-INSERT) would show up as a claim still being held.
	releaseDeadline := time.Now().Add(10 * workflowDeliveryClaimHeartbeat)
	for time.Now().Before(releaseDeadline) {
		err := repo.TakeoverExpiredRunClaim(context.Background(), runID, "probe-holder", time.Second)
		if !errors.Is(err, workflowledger.ErrClaimNotHeld) {
			t.Fatalf("claim after release: TakeoverExpiredRunClaim = %v, want ErrClaimNotHeld (a late heartbeat tick re-armed the claim)", err)
		}
		select {
		case <-time.After(workflowDeliveryClaimHeartbeat):
		}
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending after the gated failure", run.Status)
	}
}

// waitClaimHeldThroughHeartbeat polls TakeoverExpiredRunClaim under a short
// probe lease across several heartbeat intervals: an UNREFRESHED claim would
// expire and be taken over, so every probe must still fail with ErrClaimHeld
// while the delivery claim heartbeat holds the lease.
func waitClaimHeldThroughHeartbeat(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		err := repo.TakeoverExpiredRunClaim(context.Background(), runID, "probe-holder", 2*time.Second)
		if err == nil {
			t.Fatal("claim expired during the heartbeat window: takeover succeeded while the heartbeat should hold the lease")
		}
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			t.Fatalf("probe: TakeoverExpiredRunClaim = %v, want ErrClaimHeld while the claim heartbeat holds the lease", err)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	for i := 0; i < 3; i++ {
		err := repo.TakeoverExpiredRunClaim(context.Background(), runID, "probe-holder", 2*time.Second)
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			t.Fatalf("probe %d: TakeoverExpiredRunClaim = %v, want ErrClaimHeld while the claim heartbeat holds the lease", i, err)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// appendWorkflowDeliveryOnFailure adds an on_failure route to the fixture's
// [delivery] block so a failed delivery would re-enter the named step.
func appendWorkflowDeliveryOnFailure(t *testing.T, root, step string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "workflows", "two-step.toml")
	body := "on_failure = \"" + step + "\"\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowDeliverTimeoutWithOnFailureStaysPending: a hung git command that
// hits the delivery timeout is a transport fault, not a condition in the
// change. Even with delivery.on_failure set, the run must stay delivery_pending
// (retryable) and record ZERO wf-delivery repair attempts: dispatching the
// repair step for a fault that retries clean burns the run deadline for
// nothing (DC-8).
func TestWorkflowDeliverTimeoutWithOnFailureStaysPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, server.URL, storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendWorkflowDeliveryPolicy(t, root, "draft")
	appendWorkflowDeliveryOnFailure(t, root, "one")
	initWorkflowGitRepoWithOrigin(t, root)
	prRecorder := &recordingPRClient{}
	originalNewPR := WorkflowDeliverNewPR
	WorkflowDeliverNewPR = func() delivery.PRClient { return prRecorder }
	t.Cleanup(func() { WorkflowDeliverNewPR = originalNewPR })
	config := filepath.Join(root, "config.toml")

	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	originalGit := WorkflowDeliverGit
	originalTimeout := WorkflowDeliveryTimeout
	WorkflowDeliverGit = blockingGitRunner{}
	WorkflowDeliveryTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		WorkflowDeliverGit = originalGit
		WorkflowDeliveryTimeout = originalTimeout
	})

	if err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard); err == nil {
		t.Fatal("deliver error = nil, want a timeout failure")
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending: a timed-out attempt is a transport fault, not a repairable rejection", run.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	repairAttempts := 0
	for _, a := range attempts {
		if a.StepID == delivery.DeliveryRepairStepID {
			repairAttempts++
		}
	}
	if repairAttempts != 0 {
		t.Fatalf("wf-delivery repair attempts = %d, want 0: the timeout must not dispatch the on_failure repair step", repairAttempts)
	}
}

// TestWorkflowDeliveryStagePrinter pins the CLI stage printer (G11): each
// delivery stage becomes one `delivery stage=<name> detail=<detail>` line on
// stderr, and the printer is nil for io.Discard so silent CLI runs write
// nothing.
func TestWorkflowDeliveryStagePrinter(t *testing.T) {
	var stderr strings.Builder
	printStage := workflowDeliveryStagePrinter(&stderr)
	if printStage == nil {
		t.Fatal("workflowDeliveryStagePrinter(buffer) = nil, want a stage printer")
	}
	printStage("guard", "delivering run wfr-test")
	printStage("no_diff", "no diff to publish")
	want := "delivery stage=guard detail=delivering run wfr-test\ndelivery stage=no_diff detail=no diff to publish\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	if p := workflowDeliveryStagePrinter(io.Discard); p != nil {
		t.Fatalf("workflowDeliveryStagePrinter(io.Discard) = %p, want nil (silent CLI runs)", p)
	}
}

// TestWorkflowDeliverPrintsStagesToStderr pins the end-to-end wiring: the
// `workflow deliver` path passes the stderr stage printer to delivery.Deliver,
// so a successful delivery emits guard, eligibility, commit, push, pr, and
// success lines on stderr.
func TestWorkflowDeliverPrintsStagesToStderr(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)

	var stdout, stderr strings.Builder
	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("deliver error = %v; stderr = %q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("deliver stdout = %q, want status=succeeded", stdout.String())
	}
	for _, stage := range []string{"guard", "eligibility", "commit", "push", "pr", "success"} {
		if !strings.Contains(stderr.String(), "delivery stage="+stage) {
			t.Fatalf("stderr = %q, want a delivery stage=%s line", stderr.String(), stage)
		}
	}
}

// TestWorkflowDeliverRefusalRecordsReason pins the contract with
// recordDeliveryRefusal (workflow_delivery_record.go): a permanent refusal
// settled through DeliverRunWithStore must durably record the refusal reason
// (an ErrorRef resolving to the refusal text) so `workflow status` explains
// why the run settled delivery_failed.
func TestWorkflowDeliverRefusalRecordsReason(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	rewriteFixtureOriginMain(t, root)

	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "delivery base") {
		t.Fatalf("deliver error = %v, want a base-rewritten refusal", err)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusDeliveryFailed {
		t.Fatalf("run status = %q, want delivery_failed after a permanent refusal", run.Status)
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil {
		t.Fatalf("no durable delivery record after the refusal: %v", err)
	}
	if rec.ErrorRef == "" {
		t.Fatalf("delivery record = %+v, want a recorded ErrorRef naming the refusal reason", rec)
	}
	body, err := repo.LoadContent(context.Background(), rec.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "delivery base") {
		t.Fatalf("recorded refusal reason = %q, want it to name the base rewrite", body)
	}
}

// TestWorkflowDeliverPlainFailureWithoutRepairRouteRecordsCause pins the
// recording gap: a PR-metadata rejection (or any plain pre-commit delivery
// failure) on a workflow with NO repair route - no on_failure, no
// on_pr_metadata_failure - has nowhere to be routed, and validatePRMetadata
// deliberately writes no in-flight record ("travels to the classifier"), so
// settleDeliveryError's fall-through must record the failure durably.
// Without it the run sits at delivery_pending with no stored cause, and
// `workflow status` cannot say why.
func TestWorkflowDeliverPlainFailureWithoutRepairRouteRecordsCause(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	// No on_failure / on_pr_metadata_failure appended: RepairTarget resolves
	// no step for the PRMetadataError.
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	seedCLIChangeSummary(t, context.Background(), repo, runID, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	seedCLIWorkspacePRTitlePolicy(t, root, runID, repo, `[title]
pattern = '^[a-z]+\((?P<scope>[a-z]+)\): .+$'
scopes = ["feat"]
`)

	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("deliver error = %v, want the pr-title policy violation to surface", err)
	}
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// A plain error, not a refusal: the run stays retryable at
	// delivery_pending.
	if run.Status != workflowledger.RunStatusDeliveryPending {
		t.Fatalf("run status = %q, want delivery_pending", run.Status)
	}
	rec, err := repo.GetDeliveryByIdempotencyKey(context.Background(), delivery.DeliveryKey(runID, run.WorkflowDigest))
	if err != nil {
		t.Fatalf("no durable delivery record after the failure: %v", err)
	}
	if rec.ErrorRef == "" {
		t.Fatalf("delivery record = %+v, want a recorded ErrorRef naming the failure cause", rec)
	}
	body, err := repo.LoadContent(context.Background(), rec.ErrorRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "not allowed") {
		t.Fatalf("recorded failure cause = %q, want it to name the pr-title policy violation", body)
	}
}

// appendWorkflowDeliveryOnPRMetadataFailure adds an on_pr_metadata_failure
// route to the fixture's [delivery] block so a PR-metadata delivery failure
// would re-enter the named step.
func appendWorkflowDeliveryOnPRMetadataFailure(t *testing.T, root, step string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "workflows", "two-step.toml")
	body := "on_pr_metadata_failure = \"" + step + "\"\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedCLIChangeSummary records a completed step attempt whose output JSON is
// the agent's change summary (pr_title/pr_summary), so the delivery engine's
// change-summary resolution can find it.
func seedCLIChangeSummary(t *testing.T, ctx context.Context, repo workflowledger.Repository, runID, outputJSON string) {
	t.Helper()
	ref := "sha256:" + workflowledger.DigestHex([]byte(outputJSON))
	if err := repo.StoreContent(ctx, ref, []byte(outputJSON)); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		RunID: runID, StepID: "change-summary", AttemptID: "wfa-change-summary-1",
		AttemptNo: 1, Status: workflowledger.AttemptStatusSucceeded, OutputRef: ref,
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
}

// seedCLIWorkspacePRTitlePolicy writes a pr-title.toml policy under the run
// worktree's .mivia/policy directory and excludes .mivia/ from the fixture's
// index, so delivery reads the policy file but never commits it into the
// delivered diff.
func seedCLIWorkspacePRTitlePolicy(t *testing.T, root, runID string, repo workflowledger.Repository, content string) {
	t.Helper()
	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := vcs.Resolve(context.Background(), root, run.WorktreeName)
	if err != nil || worktree == nil {
		t.Fatalf("resolve run worktree = %+v, %v", worktree, err)
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open git exclude: %v", err)
	}
	if _, err := f.WriteString("\n.mivia/\n"); err != nil {
		f.Close()
		t.Fatalf("append git exclude: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close git exclude: %v", err)
	}
	dir := filepath.Join(worktree.Path, ".mivia", "policy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pr-title.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write pr-title.toml: %v", err)
	}
}

// TestWorkflowDeliverPRMetadataFailureRoutesToRepairStep: a delivery attempt
// whose PR-metadata validation fails against the workspace pr-title policy is
// a REPAIRABLE failure, so settleDeliveryError routes the run to the
// on_pr_metadata_failure step: a wf-delivery attempt is recorded whose
// ErrorRef names the policy violation, the run returns to running at the
// repair step, and no push or PR create happened (validation runs before any
// commit).
func TestWorkflowDeliverPRMetadataFailureRoutesToRepairStep(t *testing.T) {
	root, storePath, config, prRecorder := newDeliveryFixture(t)
	appendWorkflowDeliveryOnFailure(t, root, "one")
	appendWorkflowDeliveryOnPRMetadataFailure(t, root, "two")
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openDeliveryStore(t, storePath)
	seedWorktreeChange(t, root, runID, repo)
	seedCLIChangeSummary(t, context.Background(), repo, runID, `{"pr_title": "feat(scope): add widget", "pr_summary": "Adds the widget."}`)
	seedCLIWorkspacePRTitlePolicy(t, root, runID, repo, `[title]
pattern = '^[a-z]+\((?P<scope>[a-z]+)\): .+$'
scopes = ["feat"]
`)

	err := RunWorkflowWithIO([]string{"deliver", runID, "--workspace", root, "--config", config, "--allow-publish"}, io.Discard, io.Discard)
	// A first repair route returns nil: delivery.ReopenForRepair prints and CASes the
	// run, so a nil error means "routed", not "published" (the session repair
	// loop relies on the same shape).
	if err != nil {
		t.Fatalf("deliver error = %v, want the repair route to succeed", err)
	}

	run, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// The run returns to running at the on_pr_metadata_failure step, exactly
	// like an on_failure repair route.
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want %q: a PR-metadata failure must not stop the run",
			run.Status, workflowledger.RunStatusRunning)
	}
	if run.ActiveStepID != "two" {
		t.Fatalf("active step = %q, want %q (the on_pr_metadata_failure step)", run.ActiveStepID, "two")
	}

	attempts, err := repo.ListStepAttempts(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var recorded *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].StepID == delivery.DeliveryRepairStepID {
			recorded = &attempts[i]
		}
	}
	if recorded == nil {
		t.Fatal("no wf-delivery attempt recorded; the PR-metadata failure must be in the run history")
	}
	if recorded.ToStepID != "two" {
		t.Fatalf("delivery attempt route = %q, want %q", recorded.ToStepID, "two")
	}
	if recorded.ErrorRef == "" {
		t.Fatal("delivery attempt has no ErrorRef; the repair agent would have no evidence")
	}
	body, err := repo.LoadContent(context.Background(), recorded.ErrorRef)
	if err != nil {
		t.Fatalf("load failure evidence: %v", err)
	}
	if !strings.Contains(string(body), "not allowed") {
		t.Fatalf("failure evidence = %q, want it to name the pr-title policy violation", body)
	}
	// PR-metadata validation runs BEFORE any commit or push, so the boundary
	// clients must not have been called.
	if creates, finds := prRecorder.calls(); creates != 0 || finds != 0 {
		t.Fatalf("PR client calls: creates=%d finds=%d, want zero before metadata validation", creates, finds)
	}
}
