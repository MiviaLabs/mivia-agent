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
