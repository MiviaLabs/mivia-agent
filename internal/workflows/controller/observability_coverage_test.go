package controller

// Coverage tests for the workflow-observability diff lines in
// agent_step_errors.go, linear_cancel.go, linear_deadline.go,
// linear_execution.go, linear_execution_helpers.go, linear_gates.go,
// panel_step.go and progress.go. Tests only: no production edits.
//
// The failing-repository wrappers below embed workflowledger.Repository and
// override one method each so the controller exercises the exact error branch
// under test, mirroring the deadlineGetRunRepository / ctxAwareRepo seams the
// existing deadline tests use.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ---------------------------------------------------------------------------
// agent_step_errors.go: storeErrorText nil-cause short-circuit (19-20) and
// truncateTail continuation-rune walk (37-41).
// ---------------------------------------------------------------------------

func TestCoverageStoreErrorTextNilCauseReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	if got := storeErrorText(ctx, repo, nil); got != "" {
		t.Fatalf("storeErrorText(nil) = %q, want empty", got)
	}
	// The non-nil path must store the cause content-addressed (covers the
	// storeErrorText body too).
	ref := storeErrorText(ctx, repo, errors.New("boom"))
	if ref == "" {
		t.Fatal("storeErrorText(cause) returned an empty ref")
	}
	raw, err := repo.LoadContent(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "boom" {
		t.Fatalf("stored content = %q, want boom", raw)
	}
}

func TestCoverageTruncateTailWalksContinuationBytes(t *testing.T) {
	// len(s) > maxErrorTextBytes and the byte at len(s)-max is a UTF-8
	// continuation byte, forcing the start++ walk to the rune boundary.
	s := strings.Repeat("a", 3) + "é" + strings.Repeat("b", 4095)
	got := truncateTail(s, maxErrorTextBytes)
	if got != strings.Repeat("b", 4095) {
		t.Fatalf("truncateTail = %q (len %d), want the 4095-byte b tail", got, len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncateTail split a UTF-8 rune")
	}
	// Short input short-circuits before the walk (line 35).
	if short := truncateTail("ok", maxErrorTextBytes); short != "ok" {
		t.Fatalf("truncateTail(short) = %q, want ok", short)
	}
}

// ---------------------------------------------------------------------------
// linear_execution.go: truncateReason continuation-rune walk (385-389).
// ---------------------------------------------------------------------------

func TestCoverageTruncateReasonWalksContinuationBytes(t *testing.T) {
	s := strings.Repeat("a", 511) + "é" + strings.Repeat("b", 100)
	got := truncateReason(s)
	if got != strings.Repeat("a", 511) {
		t.Fatalf("truncateReason = %q (len %d), want 511 a bytes", got, len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncateReason split a UTF-8 rune")
	}
}

// ---------------------------------------------------------------------------
// linear_execution_helpers.go: failAttempt step fallback when the attempt's
// step is not declared (189-190).
// ---------------------------------------------------------------------------

func TestCoverageFailAttemptFallsBackToStepID(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	wf := &definition.CompiledWorkflow{Name: "cov-fail-attempt", InitialStep: "real", Steps: []definition.Step{{ID: "real", Kind: "agent"}}}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, nil, "wfr-cov-fail-attempt", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: ctrl.RunID, Status: workflowledger.RunStatusPending, ActiveStepID: "real"}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	storedRun, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, ctrl.RunID, storedRun.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// The attempt names a step the workflow does not declare; failAttempt must
	// fall back to a step synthesized from the attempt ID.
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-ghost-1", RunID: ctrl.RunID, StepID: "ghost", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetStepAttempt(ctx, ctrl.RunID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got, done, err := ctrl.failAttempt(ctx, current, stored, errors.New("boom"))
	if err == nil || !done || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("failAttempt = %+v, done=%v, err=%v; want failed run", got, done, err)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want one failed attempt", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("failed attempt ErrorRef is empty, want the persisted cause")
	}
}

// ---------------------------------------------------------------------------
// linear_cancel.go: nil repo, claim/CAS/list/complete error branches
// (33, 38, 55, 59, 63, 72, 84).
// ---------------------------------------------------------------------------

// coverageCancelFailRepo fails one CancelRunWithAttempts boundary per test.
type coverageCancelFailRepo struct {
	workflowledger.Repository
	mu                  sync.Mutex
	failClaim           bool
	failCASRunning      bool
	failCASCanceled     bool
	failListAttempts    bool
	failCompleteAttempt bool
	getRunCalls         int
	failGetRunCall      int // 1-based GetRun call number to fail
}

func (r *coverageCancelFailRepo) ClaimRun(ctx context.Context, runID, holder string) error {
	r.mu.Lock()
	fail := r.failClaim
	r.mu.Unlock()
	if fail {
		return errors.New("injected claim failure")
	}
	return r.Repository.ClaimRun(ctx, runID, holder)
}

func (r *coverageCancelFailRepo) CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	r.mu.Lock()
	fail := (r.failCASRunning && status == workflowledger.RunStatusRunning) || (r.failCASCanceled && status == workflowledger.RunStatusCanceled)
	r.mu.Unlock()
	if fail {
		return errors.New("injected CAS failure")
	}
	return r.Repository.CompareAndSetRunStatus(ctx, runID, expectedVersion, status, finishedAt)
}

func (r *coverageCancelFailRepo) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.mu.Lock()
	r.getRunCalls++
	fail := r.getRunCalls == r.failGetRunCall
	r.mu.Unlock()
	if fail {
		return workflowledger.RunSnapshot{}, errors.New("injected GetRun failure")
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *coverageCancelFailRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	r.mu.Lock()
	fail := r.failListAttempts
	r.mu.Unlock()
	if fail {
		return nil, errors.New("injected ListStepAttempts failure")
	}
	return r.Repository.ListStepAttempts(ctx, runID)
}

func (r *coverageCancelFailRepo) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	r.mu.Lock()
	fail := r.failCompleteAttempt
	r.mu.Unlock()
	if fail {
		return errors.New("injected CompleteStepAttempt failure")
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

func TestCoverageCancelRunNilRepo(t *testing.T) {
	ctx := context.Background()
	_, err := CancelRunWithAttempts(ctx, nil, nil, "wfr-x")
	if err == nil || !strings.Contains(err.Error(), "workflow ledger is nil") {
		t.Fatalf("CancelRunWithAttempts(nil repo) = %v, want a nil-ledger error", err)
	}
}

func TestCoverageCancelRunClaimFailure(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusPending)
	wrapped := &coverageCancelFailRepo{Repository: repo, failClaim: true}
	if _, err := CancelRunWithAttempts(ctx, wrapped, nil, runID); err == nil {
		t.Fatal("CancelRunWithAttempts = nil error, want the claim failure")
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusPending {
		t.Fatalf("run status = %q, want untouched pending after claim failure", run.Status)
	}
}

func TestCoverageCancelRunPendingCASRunningFailure(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusPending)
	wrapped := &coverageCancelFailRepo{Repository: repo, failCASRunning: true}
	if _, err := CancelRunWithAttempts(ctx, wrapped, nil, runID); err == nil {
		t.Fatal("CancelRunWithAttempts = nil error, want the pending->running CAS failure")
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusPending {
		t.Fatalf("run status = %q, want pending after the CAS failure", run.Status)
	}
}

func TestCoverageCancelRunGetRunAfterPendingCASFailure(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusPending)
	// CancelRunWithAttempts reads the run once before the pending->running CAS
	// and once after; the second read fails.
	wrapped := &coverageCancelFailRepo{Repository: repo, failGetRunCall: 2}
	if _, err := CancelRunWithAttempts(ctx, wrapped, nil, runID); err == nil {
		t.Fatal("CancelRunWithAttempts = nil error, want the post-CAS GetRun failure")
	}
}

func TestCoverageCancelRunCASCanceledFailure(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusRunning)
	wrapped := &coverageCancelFailRepo{Repository: repo, failCASCanceled: true}
	if _, err := CancelRunWithAttempts(ctx, wrapped, nil, runID); err == nil {
		t.Fatal("CancelRunWithAttempts = nil error, want the running->canceled CAS failure")
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running after the canceled CAS failure", run.Status)
	}
}

func TestCoverageCancelRunListAttemptsFailure(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusRunning)
	wrapped := &coverageCancelFailRepo{Repository: repo, failListAttempts: true}
	if _, err := CancelRunWithAttempts(ctx, wrapped, nil, runID); err == nil {
		t.Fatal("CancelRunWithAttempts = nil error, want the ListStepAttempts failure")
	}
	// D15: the run must never report canceled without first knowing whether
	// a live panel attempt needs its children reconciled first, so the
	// attempt listing that detects one now happens before the canceled CAS,
	// not after. A listing failure here must leave the run non-terminal
	// rather than risk declaring it canceled while an orphaned panel child
	// could exist unseen.
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("run status = %q, want running (unsettled) after the list failure", run.Status)
	}
}

func TestCoverageCancelRunCompleteAttemptFailureContinues(t *testing.T) {
	ctx := context.Background()
	repo, runID := newCancelRunFixture(t, workflowledger.RunStatusRunning)
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-one-1", RunID: runID, StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	wrapped := &coverageCancelFailRepo{Repository: repo, failCompleteAttempt: true}
	canceled, err := CancelRunWithAttempts(ctx, wrapped, nil, runID)
	if err != nil {
		t.Fatalf("CancelRunWithAttempts = %v; attempt marking is best-effort and must continue", err)
	}
	if len(canceled) != 0 {
		t.Fatalf("canceled attempts = %+v, want none (completion failed)", canceled)
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("attempts = %+v, want the running attempt untouched", attempts)
	}
}

// ---------------------------------------------------------------------------
// linear_deadline.go: timeoutOpenHumanAttempt completion failure (117-118).
// ---------------------------------------------------------------------------

// coverageCompleteStepFailRepo fails the Nth CompleteStepAttempt call.
type coverageCompleteStepFailRepo struct {
	workflowledger.Repository
	mu       sync.Mutex
	calls    int
	failCall int // 1-based CompleteStepAttempt call number to fail
}

func (r *coverageCompleteStepFailRepo) CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error {
	r.mu.Lock()
	r.calls++
	fail := r.calls == r.failCall
	r.mu.Unlock()
	if fail {
		return errors.New("injected CompleteStepAttempt failure")
	}
	return r.Repository.CompleteStepAttempt(ctx, runID, attemptID, expectedVersion, outcome)
}

func TestCoverageDeadlineHumanGateCompleteFailureStillTimesOut(t *testing.T) {
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	base := workflowledger.NewMemoryRepository()
	repo := &coverageCompleteStepFailRepo{Repository: base, failCall: 1}
	ctrl, err := NewLinearController(repo, &linearRunner{}, humanOnlyWorkflow(t), nil, map[string]any{"task": "x"}, "wfr-cov-deadline-complete-fail", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetTimeSource(func() time.Time { return start }); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err=%v, want waiting_approval", got, err)
	}
	ctrl.now = func() time.Time { return start.Add(2 * time.Hour) }
	settled, err := ctrl.Run(context.Background())
	if settled.Status != workflowledger.RunStatusTimedOut {
		t.Fatalf("status = %q, want timed_out", settled.Status)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expired run err = %v, want a deadline error", err)
	}
	// The human attempt completion failed (error discarded by the caller), so
	// the attempt must still be Running rather than timed_out.
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, ok := latestAttempt(attempts, "approve_me")
	if !ok {
		t.Fatal("missing human gate attempt")
	}
	if attempt.Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("human attempt status = %q, want running (completion failed)", attempt.Status)
	}
}

// ---------------------------------------------------------------------------
// linear_gates.go: command-profile gate detail (35-36), route-selection
// failure completion event (80), succeeded-route persist failure (89).
// ---------------------------------------------------------------------------

func coverageEvidenceGateWorkflow(t *testing.T, step definition.Step, transitions []definition.Transition) *definition.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "cov-gate", InitialStep: step.ID,
		Inputs:      map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits:      definition.Limits{MaxStepAttempts: 4},
		Steps:       []definition.Step{step},
		Transitions: transitions,
	}
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestCoverageEvidenceGateCommandProfileUsesKindDetail(t *testing.T) {
	// A command gate carries no named verifier, so the gate_started detail
	// falls back to the step kind.
	step := definition.Step{
		ID: "verify", Kind: "evidence_gate", OnFailure: "failure",
		Command: &definition.StepCommand{Check: "invariants", Program: "definitely-missing-verifier-xyz"},
	}
	wf := coverageEvidenceGateWorkflow(t, step, []definition.Transition{
		{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	})
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-cov-command-gate", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v, err = %v, want failed (sandboxed command cannot run)", got, err)
	}
	var started *ProgressEvent
	for _, e := range sink.take() {
		if e.Kind == ProgressGateStarted && e.StepID == "verify" {
			started = &e
		}
	}
	if started == nil {
		t.Fatal("no gate_started event for the command gate")
	}
	if started.Detail != "evidence_gate" {
		t.Fatalf("gate_started detail = %q, want the step kind evidence_gate", started.Detail)
	}
	if started.AttemptNo != 1 {
		t.Fatalf("gate_started attempt = %d, want 1", started.AttemptNo)
	}
}

func TestCoverageEvidenceGateRouteSelectionFailureFailsRun(t *testing.T) {
	// The gate passes verification (status "passed") but no transition matches
	// that output, so route selection fails and the attempt is completed failed
	// with a persisted cause and one step_completed event.
	step := definition.Step{ID: "verify", Kind: "evidence_gate", Verifier: "always-passes", OnFailure: "failure"}
	wf := coverageEvidenceGateWorkflow(t, step, []definition.Transition{
		{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"status": "approved"}}},
	})
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "always-passes", result: definition.Result{Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-cov-gate-nomatch", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v, err = %v, want failed", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok {
		t.Fatal("missing verify attempt")
	}
	if verify.Status != workflowledger.AttemptStatusFailed || verify.ToStepID != "failure" {
		t.Fatalf("verify attempt = %+v, want failed routed to failure", verify)
	}
	if verify.ErrorRef == "" {
		t.Fatal("verify attempt ErrorRef is empty, want the route-selection cause")
	}
	var failed bool
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted && e.StepID == "verify" && e.Detail == string(workflowledger.AttemptStatusFailed) {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no step_completed(failed) event for the gate attempt")
	}
}

func TestCoverageEvidenceGateSucceededRoutePersistFailureFailsRun(t *testing.T) {
	// The gate routes successfully but the attempt-completion write fails; the
	// run must fail and the attempt must stay running (never persisted).
	step := definition.Step{ID: "verify", Kind: "evidence_gate", Verifier: "always-passes", OnFailure: "failure"}
	wf := coverageEvidenceGateWorkflow(t, step, []definition.Transition{
		{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
	})
	cat := definition.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "always-passes", result: definition.Result{Status: "passed"}}); err != nil {
		t.Fatal(err)
	}
	base := workflowledger.NewMemoryRepository()
	repo := &coverageCompleteStepFailRepo{Repository: base, failCall: 1}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-cov-gate-persist", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v, err = %v, want failed", got, err)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	verify, ok := latestAttempt(attempts, "verify")
	if !ok {
		t.Fatal("missing verify attempt")
	}
	if verify.Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("verify attempt status = %q, want running (completion write failed)", verify.Status)
	}
}

// ---------------------------------------------------------------------------
// panel_step.go: stored-attempt re-read failure after create (46-47).
// ---------------------------------------------------------------------------

// coveragePanelGetStepAttemptFailRepo fails GetStepAttempt for one attempt ID.
type coveragePanelGetStepAttemptFailRepo struct {
	workflowledger.Repository
	failAttemptID string
}

func (r *coveragePanelGetStepAttemptFailRepo) GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error) {
	if attemptID == r.failAttemptID {
		return workflowledger.StepAttempt{}, errors.New("injected GetStepAttempt failure")
	}
	return r.Repository.GetStepAttempt(ctx, runID, attemptID)
}

func TestCoveragePanelStepGetStoredAttemptFailure(t *testing.T) {
	step := definition.Step{
		ID: "review", Kind: "agent_panel", Context: []definition.ContextBinding{{From: "inputs.task", As: "task", MaxBytes: 1024}},
		Panel: &definition.AgentPanel{FailurePolicy: "require_all", Members: []definition.PanelMember{
			{ID: "security", Agent: "panel-reviewer", Skill: "secure-change", Template: "security", OutputSchema: "report"},
			{ID: "correctness", Agent: "panel-reviewer", Skill: "bug-audit", Template: "correctness", OutputSchema: "report"},
		}},
	}
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		PanelBindings: map[string]workflowledger.PanelBindingSnapshot{
			"review/security":    {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("a", 64), ProviderName: "deepseek", Model: "deepseek-v4-flash"},
			"review/correctness": {AgentName: "panel-reviewer", AgentDigest: strings.Repeat("b", 64), ProviderName: "zai", Model: "glm-5.2"},
		},
		Templates: map[string]workflowledger.RefSnapshot{"security": {Bytes: []byte("Review {{inputs.task}}.")}, "correctness": {Bytes: []byte("Review {{inputs.task}}.")}},
		Schemas:   map[string]workflowledger.RefSnapshot{"report": {Bytes: []byte(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := workflowledger.NewMemoryRepository()
	repo := &coveragePanelGetStepAttemptFailRepo{Repository: base, failAttemptID: "wfa-review-1"}
	wf := &definition.CompiledWorkflow{Name: "cov-panel", InitialStep: "review", Steps: []definition.Step{step}}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "change"}, "wfr-cov-panel-get-fail", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err == nil || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run = %+v, err = %v, want failed", got, err)
	}
	// The panel attempt was created but the re-read failed, so it stays running.
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].AttemptID != "wfa-review-1" || attempts[0].Status != workflowledger.AttemptStatusRunning {
		t.Fatalf("attempts = %+v, want wfa-review-1 running", attempts)
	}
}

// ---------------------------------------------------------------------------
// progress.go: ProgressEvent.String (47-49) and the exported EmitProgress
// entry point (76-78).
// ---------------------------------------------------------------------------

func TestCoverageProgressEventStringRendersFields(t *testing.T) {
	e := ProgressEvent{
		Kind: ProgressStepCompleted, RunID: "wfr-1", StepID: "build", AttemptNo: 2, Detail: "succeeded",
	}
	got := e.String()
	for _, want := range []string{"step_completed", "wfr-1", "build", "attempt=2", "succeeded"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ProgressEvent.String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestCoverageEmitProgressExportedEntryPoint(t *testing.T) {
	now := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	sink := &recordingProgressSink{}
	ctrl := progressController(t, "wfr-cov-emit-exported", sink)
	if err := ctrl.SetTimeSource(func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	ctrl.EmitProgress(ProgressEvent{Kind: ProgressRunFinished, Detail: "succeeded"})
	events := sink.take()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Kind != ProgressRunFinished || events[0].RunID != "wfr-cov-emit-exported" || !events[0].Timestamp.Equal(now) {
		t.Fatalf("emitted event = %+v, want run ID stamped and clock timestamp", events[0])
	}
}
