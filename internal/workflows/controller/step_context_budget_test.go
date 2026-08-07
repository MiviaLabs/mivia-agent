package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// TestStepContextBudgetFitsMultipleEvidenceBindings pins that a step can bind
// several legal evidence values (each ≤ definition.MaxEvidenceBindingBytes)
// and render within maxStepContextBytes. Before the fix the render cap was the
// same size as one binding, so two legal 25KB bindings failed with "rendered
// template exceeds 32768 bytes" and the run died deterministically.
func TestStepContextBudgetFitsMultipleEvidenceBindings(t *testing.T) {
	a := strings.Repeat("a", 25<<10)
	b := strings.Repeat("b", 25<<10)
	rendered, err := template.Render("{{evidence.a}}\n{{evidence.b}}", nil, map[string]any{"a": a, "b": b}, definition.MaxEvidenceBindingBytes, maxStepContextBytes)
	if err != nil {
		t.Fatalf("two legal evidence bindings must render within the step context budget: %v", err)
	}
	if len(rendered) < 2*(25<<10) {
		t.Fatalf("rendered %d bytes, want both bindings present", len(rendered))
	}
}

// TestStepContextBudgetExceedsSingleBinding pins the invariant behind the fix:
// a step may bind more than one legal value, so the aggregate context budget
// must exceed a single binding cap.
func TestStepContextBudgetExceedsSingleBinding(t *testing.T) {
	if maxStepContextBytes <= definition.MaxEvidenceBindingBytes {
		t.Fatalf("maxStepContextBytes (%d) must exceed MaxEvidenceBindingBytes (%d)", maxStepContextBytes, definition.MaxEvidenceBindingBytes)
	}
}

// TestEvidenceBindingStillCapped pins the per-binding cap is still enforced
// after the aggregate cap was raised.
func TestEvidenceBindingStillCapped(t *testing.T) {
	big := strings.Repeat("a", definition.MaxEvidenceBindingBytes+1)
	if _, err := template.Render("{{evidence.a}}", nil, map[string]any{"a": big}, definition.MaxEvidenceBindingBytes, maxStepContextBytes); err == nil {
		t.Fatal("expected oversized evidence binding to be rejected")
	}
}

// TestEvidenceSelectionOverLedgerCapFailsBeforeDispatch pins the persistence
// cap fix: the ledger stores evidence-selection metadata under
// workflowledger.MaxEvidenceBytes (16KiB), but marshalEvidenceSelection used
// to cap only at spec.MaxContextBytes (256KiB), so an oversized selection ran
// the agent to completion and only then failed at persistence. It must fail
// BEFORE dispatch, and no child run may be created.
func TestEvidenceSelectionOverLedgerCapFailsBeforeDispatch(t *testing.T) {
	base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
	observed := &inspectingCoordinator{Coordinator: base}
	spec := validStepRequest()
	spec.MaxContextBytes = 256 << 10
	// Enough small bindings that the selection metadata (name/source/bytes/
	// digest per item) exceeds the ledger's 16KiB persistence cap while every
	// binding itself stays well under the per-binding cap.
	spec.Inputs = make(map[string]any, 401)
	spec.Inputs["task"] = "build"
	for i := 0; i < 400; i++ {
		spec.Inputs[fmt.Sprintf("k%03d", i)] = "x"
	}
	_, err := NewCoordinatorRunner(observed).RunStep(context.Background(), spec)
	if err == nil {
		t.Fatal("evidence selection over the ledger cap must fail before dispatch")
	}
	want := fmt.Sprintf("evidence selection exceeds %d bytes", workflowledger.MaxEvidenceBytes)
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if observed.ensure.RunID != "" {
		t.Fatalf("child run %q was created despite oversized evidence selection (dispatch happened)", observed.ensure.RunID)
	}
}

// TestCoordinatorRunnerEnsuresNonInteractiveParent pins that the workflow
// controller is a non-interactive parent: the EnsureRun request must carry
// NonInteractiveParent so a child's parked questions are auto-declined at park
// time instead of burning the child's full wait budget.
func TestCoordinatorRunnerEnsuresNonInteractiveParent(t *testing.T) {
	base := stepRunner(t, stepHandler{out: json.RawMessage(`{"ok":true}`)}).Coordinator
	observed := &inspectingCoordinator{Coordinator: base}
	if _, err := NewCoordinatorRunner(observed).RunStep(context.Background(), validStepRequest()); err != nil {
		t.Fatal(err)
	}
	if !observed.ensure.NonInteractiveParent {
		t.Fatalf("EnsureRun request = %+v, want NonInteractiveParent=true", observed.ensure)
	}
}
