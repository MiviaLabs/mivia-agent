package controller

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// TestStepContextBudgetFitsMultipleEvidenceBindings pins that a step can bind
// several legal evidence values (each ≤ maxEvidenceBindingBytes) and render
// within maxStepContextBytes. Before the fix the render cap was the same size
// as one binding, so two legal 25KB bindings failed with "rendered template
// exceeds 32768 bytes" and the run died deterministically.
func TestStepContextBudgetFitsMultipleEvidenceBindings(t *testing.T) {
	a := strings.Repeat("a", 25<<10)
	b := strings.Repeat("b", 25<<10)
	rendered, err := template.Render("{{evidence.a}}\n{{evidence.b}}", nil, map[string]any{"a": a, "b": b}, maxEvidenceBindingBytes, maxStepContextBytes)
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
	if maxStepContextBytes <= maxEvidenceBindingBytes {
		t.Fatalf("maxStepContextBytes (%d) must exceed maxEvidenceBindingBytes (%d)", maxStepContextBytes, maxEvidenceBindingBytes)
	}
}

// TestEvidenceBindingStillCapped pins the per-binding cap is still enforced
// after the aggregate cap was raised.
func TestEvidenceBindingStillCapped(t *testing.T) {
	big := strings.Repeat("a", maxEvidenceBindingBytes+1)
	if _, err := template.Render("{{evidence.a}}", nil, map[string]any{"a": big}, maxEvidenceBindingBytes, maxStepContextBytes); err == nil {
		t.Fatal("expected oversized evidence binding to be rejected")
	}
}
