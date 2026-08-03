package config

import (
	"strings"
	"testing"
)

// [tools] batch_result_budget_bytes - the aggregate per-batch tool-result
// budget (plan tools/06). Off by default: an operator who never heard of it
// must get today's behaviour exactly.

func TestBatchResultBudgetDefaultsToOff(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.BatchResultBudgetBytes != BatchResultBudgetOff {
		t.Fatalf("unset batch_result_budget_bytes resolved to %d, want %d (off)",
			res.Tools.BatchResultBudgetBytes, BatchResultBudgetOff)
	}
}

func TestBatchResultBudgetAcceptsALiteralBudget(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "batch_result_budget_bytes = 262144")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.BatchResultBudgetBytes != 262144 {
		t.Fatalf("resolved to %d, want 262144", res.Tools.BatchResultBudgetBytes)
	}
}

func TestBatchResultBudgetNegativeNormalizesToDerived(t *testing.T) {
	for _, line := range []string{"batch_result_budget_bytes = -1", "batch_result_budget_bytes = -99"} {
		res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, line)})
		if err != nil {
			t.Fatalf("%s rejected: %v", line, err)
		}
		if res.Tools.BatchResultBudgetBytes != BatchResultBudgetDerived {
			t.Fatalf("%s resolved to %d, want %d (derived)",
				line, res.Tools.BatchResultBudgetBytes, BatchResultBudgetDerived)
		}
	}
}

// A budget under the degrade floor cannot hold: the first oversized result is
// re-cut to the floor regardless, so the bound would be a fiction.
func TestBatchResultBudgetUnderTheFloorIsALoadError(t *testing.T) {
	_, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "batch_result_budget_bytes = 4096")})
	if err == nil {
		t.Fatal("batch_result_budget_bytes = 4096 was accepted; want a load error")
	}
	if !strings.Contains(err.Error(), "batch_result_budget_bytes") {
		t.Fatalf("error %q does not name the key", err)
	}
}

func TestBatchResultBudgetAtTheFloorIsAccepted(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolResultCapConfig(t, "batch_result_budget_bytes = 16384")})
	if err != nil {
		t.Fatalf("the floor value itself was rejected: %v", err)
	}
	if res.Tools.BatchResultBudgetBytes != MinBatchResultBudgetBytes {
		t.Fatalf("resolved to %d, want %d", res.Tools.BatchResultBudgetBytes, MinBatchResultBudgetBytes)
	}
}
