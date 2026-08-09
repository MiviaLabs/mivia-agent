package controller

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
)

// humanGateProgressWorkflow compiles a single human_gate workflow whose
// approval routes to the success terminal.
func humanGateProgressWorkflow(t *testing.T) *compiler.CompiledWorkflow {
	t.Helper()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "human-progress", InitialStep: "approve_me",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps: []definition.Step{
			{ID: "approve_me", Kind: "human_gate", OnFailure: "failure"},
		},
		Transitions: []definition.Transition{
			{From: "approve_me", To: "success", Match: definition.MatchCriteria{Status: "succeeded", Output: map[string]string{"decision": "approved"}}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// TestHumanGateParkEmitsApprovalRequested: parking a human gate must emit one
// ProgressApprovalRequested event carrying the approval id.
func TestHumanGateParkEmitsApprovalRequested(t *testing.T) {
	wf := humanGateProgressWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-progress", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err = %v, want waiting_approval", got, err)
	}
	events := sink.take()
	var requested []ProgressEvent
	for _, e := range events {
		if e.Kind == ProgressApprovalRequested {
			requested = append(requested, e)
		}
	}
	if len(requested) != 1 {
		t.Fatalf("approval_requested events = %d, want 1: %+v", len(requested), events)
	}
	wantID := PendingApprovalID("approve_me", 1)
	if requested[0].Detail != wantID {
		t.Fatalf("approval_requested detail = %q, want %q", requested[0].Detail, wantID)
	}
	if requested[0].StepID != "approve_me" || requested[0].AttemptNo != 1 {
		t.Fatalf("approval_requested identity = %+v", requested[0])
	}
	if requested[0].RunID != ctrl.RunID {
		t.Fatalf("approval_requested run ID = %q, want %q", requested[0].RunID, ctrl.RunID)
	}
}

// TestApproveHumanGateEmitsExactlyOneStepCompleted: approving a parked gate
// must emit exactly one ProgressStepCompleted with the resolved status.
func TestApproveHumanGateEmitsExactlyOneStepCompleted(t *testing.T) {
	wf := humanGateProgressWorkflow(t)
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-human-complete", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	if got, err := ctrl.Run(context.Background()); err != nil || got.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run = %+v, err = %v, want waiting_approval", got, err)
	}
	approvalID := PendingApprovalID("approve_me", 1)
	if err := ctrl.Approve(context.Background(), approvalID, "operator"); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("after approve = %+v, err = %v, want succeeded", got, err)
	}
	events := sink.take()
	var completed []ProgressEvent
	for _, e := range events {
		if e.Kind == ProgressStepCompleted {
			completed = append(completed, e)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("step_completed events = %d, want exactly 1: %+v", len(completed), events)
	}
	if completed[0].Detail != "succeeded" {
		t.Fatalf("step_completed detail = %q, want succeeded", completed[0].Detail)
	}
	if completed[0].StepID != "approve_me" || completed[0].AttemptNo != 1 {
		t.Fatalf("step_completed identity = %+v", completed[0])
	}
}

// TestEvidenceGateSuccessEmitsExactlyOneStepCompleted: an evidence gate that
// passes must emit exactly one ProgressStepCompleted with the succeeded status
// (before the fix the attempt reached terminal with only gate_started).
func TestEvidenceGateSuccessEmitsExactlyOneStepCompleted(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-progress-success", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "go-check", OnFailure: "failure"}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(trailProfile{name: "go-check", trail: &progressTrail{}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence-success", []byte("snap"))
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
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err = %v, want succeeded", got, err)
	}
	var completed []ProgressEvent
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted {
			completed = append(completed, e)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("step_completed events = %d, want exactly 1: %+v", len(completed), sink.take())
	}
	if completed[0].StepID != "verify" || completed[0].AttemptNo != 1 || completed[0].Detail != "succeeded" {
		t.Fatalf("step_completed event = %+v, want verify attempt 1 succeeded", completed[0])
	}
}

// TestEvidenceGateFailureEmitsExactlyOneStepCompletedFailed: a repairable
// failed verification that routes to a repair step must emit exactly one
// ProgressStepCompleted with the failed status for the gate attempt.
func TestEvidenceGateFailureEmitsExactlyOneStepCompletedFailed(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-failure-progress", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 8},
		Steps: []definition.Step{
			{ID: "verify", Kind: "evidence_gate", Verifier: "lint-check", OnFailure: "repair"},
			{ID: "repair", Kind: "agent", Agent: "dev"},
		},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
			{From: "verify", To: "repair", Match: definition.MatchCriteria{Status: "failed"}},
			{From: "repair", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "lint-check", result: verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{{Name: "lint", Status: "failed", Class: "lint", Detail: "lint errors"}},
	}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{outputs: map[string]json.RawMessage{"repair": json.RawMessage(`{"ok":true}`)}}, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, map[string]any{"task": "x"}, "wfr-evidence-failure", []byte("snap"))
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
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err = %v, want succeeded after repair", got, err)
	}
	var completed []ProgressEvent
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted {
			completed = append(completed, e)
		}
	}
	if len(completed) != 2 {
		t.Fatalf("step_completed events = %d, want exactly 2: %+v", len(completed), completed)
	}
	if completed[0].StepID != "verify" || completed[0].AttemptNo != 1 || completed[0].Detail != "failed" {
		t.Fatalf("gate step_completed event = %+v, want verify attempt 1 failed", completed[0])
	}
	if completed[1].StepID != "repair" || completed[1].Detail != "succeeded" {
		t.Fatalf("repair step_completed event = %+v, want repair succeeded", completed[1])
	}
}

// TestEvidenceGateHostFailureEmitsExactlyOneStepCompletedFailed: a host-class
// verification failure settles through settleHostFailure and must emit exactly
// one ProgressStepCompleted with the failed status.
func TestEvidenceGateHostFailureEmitsExactlyOneStepCompletedFailed(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-host-progress", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "host-check"}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	if err := cat.Register(fixedVerifierProfile{name: "host-check", result: verifier.Result{
		Status: "failed",
		Checks: []verifier.Check{{Name: "sandbox", Status: "failed", Class: "host", Detail: "sandbox unavailable"}},
	}}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	sink := &recordingProgressSink{}
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence-host", []byte("snap"))
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
	var completed []ProgressEvent
	for _, e := range sink.take() {
		if e.Kind == ProgressStepCompleted {
			completed = append(completed, e)
		}
	}
	if len(completed) != 1 {
		t.Fatalf("step_completed events = %d, want exactly 1: %+v", len(completed), completed)
	}
	if completed[0].StepID != "verify" || completed[0].AttemptNo != 1 || completed[0].Detail != "failed" {
		t.Fatalf("step_completed event = %+v, want verify attempt 1 failed", completed[0])
	}
}

// progressTrail records emitted events and verifier calls in one ordered list.
type progressTrail struct {
	mu    sync.Mutex
	items []any
}

// trailSink appends each emitted event to the shared trail.
type trailSink struct{ trail *progressTrail }

// Emit appends the event to the shared trail.
func (s trailSink) Emit(e ProgressEvent) {
	s.trail.mu.Lock()
	defer s.trail.mu.Unlock()
	s.trail.items = append(s.trail.items, e)
}

// trailProfile appends a marker when verification runs.
type trailProfile struct {
	name  string
	trail *progressTrail
}

// Name returns the registered verifier name.
func (p trailProfile) Name() string { return p.name }

// Verify records that verification ran, after the gate event.
func (p trailProfile) Verify(context.Context, verifier.Request) (verifier.Result, error) {
	p.trail.mu.Lock()
	defer p.trail.mu.Unlock()
	p.trail.items = append(p.trail.items, "verify-ran")
	return verifier.Result{Status: "succeeded"}, nil
}

// TestEvidenceGateEmitsGateStartedBeforeVerify: an evidence gate must emit
// ProgressGateStarted before the verifier profile runs.
func TestEvidenceGateEmitsGateStartedBeforeVerify(t *testing.T) {
	wf := &definition.WorkflowFile{
		Version: 1, Name: "evidence-progress", InitialStep: "verify",
		Inputs: map[string]definition.InputDef{"task": {Type: "string", Required: true}},
		Limits: definition.Limits{MaxStepAttempts: 4},
		Steps:  []definition.Step{{ID: "verify", Kind: "evidence_gate", Verifier: "go-check", OnFailure: "failure"}},
		Transitions: []definition.Transition{
			{From: "verify", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
	}
	compiled, err := compiler.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	cat := verifier.NewCatalogue()
	trail := &progressTrail{}
	if err := cat.Register(trailProfile{name: "go-check", trail: trail}); err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, nil, map[string]any{"task": "x"}, "wfr-evidence-progress", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetVerifiers(cat); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetProgressSink(trailSink{trail: trail}); err != nil {
		t.Fatal(err)
	}
	got, err := ctrl.Run(context.Background())
	if err != nil || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("run = %+v, err = %v, want succeeded", got, err)
	}
	trail.mu.Lock()
	defer trail.mu.Unlock()
	if len(trail.items) < 2 {
		t.Fatalf("trail = %v, want gate_started before verify-ran", trail.items)
	}
	started, ok := trail.items[0].(ProgressEvent)
	if !ok || started.Kind != ProgressGateStarted {
		t.Fatalf("trail[0] = %v, want ProgressGateStarted", trail.items[0])
	}
	if started.StepID != "verify" || started.AttemptNo != 1 || started.Detail != "go-check" {
		t.Fatalf("gate_started event = %+v, want verify attempt 1 detail go-check", started)
	}
	if trail.items[1] != "verify-ran" {
		t.Fatalf("trail[1] = %v, want verify-ran after gate_started", trail.items[1])
	}
}
