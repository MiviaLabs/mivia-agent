package controller

import (
	"encoding/json"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestCloneValuesPreservesLargeIntegerInputs pins that NewLinearController's
// input clone round-trips json.Number values exactly. The plain json.Unmarshal
// into map[string]any decoded numbers as float64, silently rounding integers
// >= 2^53 (9007199254740993 -> 9007199254740992) before they reach the agent
// prompt. The clone must use a UseNumber decoder so large integers survive
// verbatim.
func TestCloneValuesPreservesLargeIntegerInputs(t *testing.T) {
	inputs := map[string]any{
		"bigNumber": json.Number("9007199254740993"),
		"bigInt":    int64(9007199254740993),
		"smallInt":  int64(42),
	}
	cloned := cloneValues(inputs)

	big, ok := cloned["bigNumber"].(json.Number)
	if !ok {
		t.Fatalf("bigNumber = %T (%v), want json.Number", cloned["bigNumber"], cloned["bigNumber"])
	}
	if big.String() != "9007199254740993" {
		t.Fatalf("bigNumber = %s, want 9007199254740993", big.String())
	}
	raw, err := json.Marshal(cloned)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bigNumber":9007199254740993`, `"bigInt":9007199254740993`, `"smallInt":42`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("cloned map %s does not preserve %s", raw, want)
		}
	}
}

// TestNewLinearControllerPreservesLargeIntegerInput pins the end-to-end path:
// inputs admitted through NewLinearController must keep the exact integer so
// the agent prompt sees the requested value, not a float64-rounded one.
func TestNewLinearControllerPreservesLargeIntegerInput(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, &linearRunner{}, linearWorkflow(t), nil, map[string]any{
		"n": json.Number("9007199254740993"),
	}, "wfr-inputs-exact", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	n, ok := ctrl.Inputs["n"].(json.Number)
	if !ok {
		t.Fatalf("input n = %T (%v), want json.Number", ctrl.Inputs["n"], ctrl.Inputs["n"])
	}
	if n.String() != "9007199254740993" {
		t.Fatalf("input n = %s, want 9007199254740993", n.String())
	}
}
