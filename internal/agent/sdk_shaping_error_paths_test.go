package agent

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// TestWrapTurnShaping_NilInnerOrTurnIsANoOp pins the earliest guard.
func TestWrapTurnShaping_NilInnerOrTurnIsANoOp(t *testing.T) {
	cliTool := &fakeTool{name: "t1"}
	if got := wrapTurnShaping(nil, cliTool, Options{BatchResultBudgetBytes: 100}, newSDKTurnState()); got != nil {
		t.Error("wrapTurnShaping with a nil inner must return nil unchanged")
	}
}

// resultBudgetFakeTool adds ResultBudgetBytes to fakeTool so
// wrapTurnShaping's resultBudgetTool type assertion succeeds.
type resultBudgetFakeTool struct {
	fakeTool
	budget int
}

func (t *resultBudgetFakeTool) ResultBudgetBytes() int { return t.budget }

// TestWrapTurnShaping_ResultBudgetToolCapIsHonored pins the
// resultBudgetTool type-assertion success branch: a cliTool declaring its
// own per-result cap must have that cap threaded into the wrapper, not the
// batch default.
func TestWrapTurnShaping_ResultBudgetToolCapIsHonored(t *testing.T) {
	inner, err := sdkadapter.ConvertTool(&fakeTool{name: "capped"})
	if err != nil {
		t.Fatal(err)
	}
	cliTool := &resultBudgetFakeTool{fakeTool: fakeTool{name: "capped"}, budget: 42}
	turn := newSDKTurnState()
	wrapped := wrapTurnShaping(inner, cliTool, Options{BatchResultBudgetBytes: 100}, turn)
	shim, ok := wrapped.(*turnShapeWrapper)
	if !ok {
		t.Fatalf("wrapTurnShaping did not return a *turnShapeWrapper: %T", wrapped)
	}
	if shim.cap != 42 {
		t.Errorf("perResultCap = %d, want the resultBudgetTool's own 42", shim.cap)
	}
}
