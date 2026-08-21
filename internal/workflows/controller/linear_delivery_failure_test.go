package controller

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// createDeliveryHintRun admits a run so the ledger can carry (or omit) a
// wf-delivery failure attempt for the delivery.failure binding tests.
func createDeliveryHintRun(t *testing.T, repo workflowledger.Repository, runID string) {
	t.Helper()
	snap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "repair"}, snap); err != nil {
		t.Fatal(err)
	}
}

// seedDeliveryFailureAttempt stores text as the error content of a wf-delivery
// repair attempt, exactly how the cli records a failed delivery attempt.
func seedDeliveryFailureAttempt(t *testing.T, repo workflowledger.Repository, runID, text string) {
	t.Helper()
	ref := "sha256:" + workflowledger.DigestHex([]byte(text))
	if err := repo.StoreContent(context.Background(), ref, []byte(text)); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-delivery-1", RunID: runID,
		StepID: delivery.DeliveryRepairStepID, AttemptNo: 1, ErrorRef: ref,
	}
	if err := repo.CreateStepAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
}

// deliveryHintRequest renders one agent step whose context binds
// delivery.failure, returning the dispatched request.
func deliveryHintRequest(t *testing.T, repo workflowledger.Repository, runID string, maxBytes int) AgentStepRequest {
	t.Helper()
	runtime := StepRuntime{Agent: agents.ResolvedAgent{Name: "worker"}, Template: "hint={{evidence.delivery_hint}}"}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), map[string]StepRuntime{"repair": runtime}, nil, runID, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	step := definition.Step{ID: "repair", Kind: "agent", Context: []definition.ContextBinding{
		{From: "delivery.failure", As: "delivery_hint", MaxBytes: maxBytes, Optional: true},
	}}
	attempt := workflowledger.StepAttempt{AttemptID: "wfa-repair-1", RunID: runID, StepID: "repair", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
	req, err := ctrl.agentStepRequest(context.Background(), workflowledger.RunSnapshot{RunID: runID}, step, runtime, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestDeliveryFailureBindingRendersHint pins the harness contract: the repair
// step receives the delivery failure hint DIRECTLY in its rendered prompt, so
// the agent never fetches it.
func TestDeliveryFailureBindingRendersHint(t *testing.T) {
	const runID = "wfr-delivery-hint"
	const failureText = "delivery failed: remote rejected the branch; rebase and retry"
	repo := workflowledger.NewMemoryRepository()
	createDeliveryHintRun(t, repo, runID)
	seedDeliveryFailureAttempt(t, repo, runID, failureText)

	req := deliveryHintRequest(t, repo, runID, 4096)
	if !strings.Contains(req.Prompt, "hint="+failureText) {
		t.Fatalf("prompt %q does not carry the delivery failure hint", req.Prompt)
	}
}

// TestDeliveryFailureBindingEmptyWithoutAttempt pins the optional-absent
// semantics: with no wf-delivery failure attempt the binding renders as an
// empty string and the step does not error.
func TestDeliveryFailureBindingEmptyWithoutAttempt(t *testing.T) {
	const runID = "wfr-delivery-hint-empty"
	repo := workflowledger.NewMemoryRepository()
	createDeliveryHintRun(t, repo, runID)

	req := deliveryHintRequest(t, repo, runID, 4096)
	got, ok := req.Evidence["delivery_hint"].(string)
	if !ok || got != "" {
		t.Fatalf("evidence[delivery_hint] = %#v, want an empty string", req.Evidence["delivery_hint"])
	}
}

// TestDeliveryFailureBindingTruncatesRuneSafe pins the byte cap: when the
// failure text exceeds max_bytes, the prompt receives a rune-safe prefix and
// the bound value is never invalid UTF-8.
func TestDeliveryFailureBindingTruncatesRuneSafe(t *testing.T) {
	const runID = "wfr-delivery-hint-trunc"
	const maxBytes = 32
	failureText := "prefix" + strings.Repeat("界", 40) // multibyte runes past the cap
	repo := workflowledger.NewMemoryRepository()
	createDeliveryHintRun(t, repo, runID)
	seedDeliveryFailureAttempt(t, repo, runID, failureText)

	req := deliveryHintRequest(t, repo, runID, maxBytes)
	got, ok := req.Evidence["delivery_hint"].(string)
	if !ok {
		t.Fatalf("evidence[delivery_hint] = %#v (%T), want a string", req.Evidence["delivery_hint"], req.Evidence["delivery_hint"])
	}
	if len(got) > maxBytes {
		t.Fatalf("hint = %d bytes, want <= %d", len(got), maxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("hint %q is not valid UTF-8", got)
	}
	if !strings.HasPrefix(got, "prefix") {
		t.Fatalf("truncated hint %q must keep the text prefix", got)
	}
}

// staleDeliveryZombieFixture builds the exact pre-atomic-re-entry crash
// artifact: a run CAS'd to running with a Running wf-delivery attempt that
// has no child identity and no declared step, over a compiled workflow that
// declares the repair step and routes delivery failure to it. Returns the
// repository, run id, the recorded zombie attempt, and the controller whose
// resume join must heal it.
func staleDeliveryZombieFixture(t *testing.T, wf *definition.WorkflowFile) (workflowledger.Repository, string, workflowledger.StepAttempt, *LinearController) {
	t.Helper()
	ctx := context.Background()
	compiled, err := definition.Compile(wf)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-delivery-zombie"
	snap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "repair"}, snap); err != nil {
		t.Fatal(err)
	}
	// The run is CAS'd to running, exactly as ReopenForRepair leaves it before
	// writing the re-entry attempt.
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	// Seed the exact crash artifact: a Running wf-delivery attempt with no
	// child identity (ReopenForRepair never dispatches a child).
	zombie := workflowledger.StepAttempt{
		AttemptID: "wfa-wf-delivery-1", RunID: runID,
		StepID: delivery.DeliveryRepairStepID, AttemptNo: 1,
	}
	if err := repo.CreateStepAttempt(ctx, zombie); err != nil {
		t.Fatal(err)
	}
	recorded, err := repo.GetStepAttempt(ctx, runID, zombie.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, compiled, map[string]StepRuntime{
		"repair": {Agent: agents.ResolvedAgent{Name: "dev"}},
	}, nil, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	return repo, runID, recorded, ctrl
}

// TestJoinInFlightAttemptHealsStaleDeliveryReentry pins the resume heal for
// the wf-delivery repair crash zombie. The resume join used to hard-error
// "workflow step \"wf-delivery\" is not declared", which parked the run
// permanently (unresumable, undeliverable — only cancellable). The heal
// completes the zombie as Failed with the workflow's repair route under
// version CAS, so the resume proceeds at the derived repair step.
func TestJoinInFlightAttemptHealsStaleDeliveryReentry(t *testing.T) {
	ctx := context.Background()
	wf := &definition.WorkflowFile{
		Version: 1, Name: "delivery-repair-zombie", InitialStep: "repair",
		Steps: []definition.Step{
			{ID: "repair", Kind: "agent", Agent: "dev",
				Context: []definition.ContextBinding{{From: "delivery.failure", As: "delivery_hint", MaxBytes: 8192, Optional: true}}},
		},
		Transitions: []definition.Transition{
			{From: "repair", To: "success", Match: definition.MatchCriteria{Status: "succeeded"}},
		},
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main", OnFailure: "repair",
		},
	}
	repo, runID, recorded, ctrl := staleDeliveryZombieFixture(t, wf)

	// Regression: this used to error "workflow step \"wf-delivery\" is not
	// declared", making the run permanently unresumable and undeliverable.
	if err := ctrl.JoinInFlightAttempt(ctx, recorded); err != nil {
		t.Fatalf("JoinInFlightAttempt() error = %v, want nil (heal the stale delivery re-entry)", err)
	}

	// The zombie is now terminal Failed with the workflow's repair route.
	healed, err := repo.GetStepAttempt(ctx, runID, recorded.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if healed.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("healed attempt status = %q, want %q", healed.Status, workflowledger.AttemptStatusFailed)
	}
	if healed.ToStepID != "repair" {
		t.Fatalf("healed attempt route = %q, want repair", healed.ToStepID)
	}
	// Nothing remains in flight, and the derived active step is the repair
	// step the resume will dispatch.
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Fatalf("PlanResume.AttemptsInFlight = %d, want 0 after the heal", len(plan.AttemptsInFlight))
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ActiveStepID != "repair" {
		t.Fatalf("derived active step = %q, want repair", run.ActiveStepID)
	}
}

// TestJoinInFlightAttemptKeepsHardErrorForOtherUndeclaredSteps pins the gate
// boundary of the heal: ONLY the wf-delivery re-entry zombie is healed. Any
// other undeclared in-flight step keeps the hard "not declared" error, so the
// heal cannot mask a genuinely undeclared step.
func TestJoinInFlightAttemptKeepsHardErrorForOtherUndeclaredSteps(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-undeclared-step"
	snap, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "declared"}, snap); err != nil {
		t.Fatal(err)
	}
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), map[string]StepRuntime{}, nil, runID, []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{
		AttemptID: "wfa-mystery-1", RunID: runID,
		StepID: "mystery", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning,
	}
	err = ctrl.JoinInFlightAttempt(ctx, attempt)
	if err == nil || !strings.Contains(err.Error(), "is not declared") {
		t.Fatalf("JoinInFlightAttempt() error = %v, want the hard undeclared-step error for a non-wf-delivery step", err)
	}
}
