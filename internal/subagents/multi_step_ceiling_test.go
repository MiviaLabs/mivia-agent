package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// floorDerivedCeiling mirrors runtime's unexported derivation for a tool that
// declares nothing above the historical 256KiB floor:
// 262144 + 65536 (Policy default MaxInputBytes) + 4096 (framing slack).
const floorDerivedCeiling = 262144 + 65536 + 4096

// budgetedProbeTool is a SYNTHETIC tool with a FABRICATED result budget. Real
// tool budgets move with config and with the tools package; pinning scoped
// dispatcher behaviour to them would make this test re-fail for reasons that
// have nothing to do with sub-agent scoping.
type budgetedProbeTool struct {
	name   string
	budget int
}

func (t *budgetedProbeTool) Name() string               { return t.name }
func (t *budgetedProbeTool) Description() string        { return "synthetic ceiling probe" }
func (t *budgetedProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *budgetedProbeTool) ResultBudgetBytes() int     { return t.budget }
func (t *budgetedProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return strings.Repeat("x", 16), nil
}

func probeRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&budgetedProbeTool{name: "big_budget", budget: 8 << 20})
	reg.Register(&budgetedProbeTool{name: "small_budget", budget: 262144})
	return reg
}

// TestScopedDispatcherDerivesPerToolCeilings: newScopedLoop builds a nested
// dispatcher from the restricted registry and the PARENT's policy. The parent
// policy carries the parent's global cap, which a single generous tool budget
// inflates - so without per-tool derivation the nested dispatcher hands the
// small tool the big tool's slack, exactly the defect this change removes.
// The scoped dispatcher must bound each tool by that tool's own declaration.
func TestScopedDispatcherDerivesPerToolCeilings(t *testing.T) {
	reg := probeRegistry()
	parent, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()

	h := &MultiStepHandler{FullRegistry: reg, Dispatcher: parent}
	scoped, err := h.newScopedLoop()
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.dispatcher.Close()

	if got := scoped.dispatcher.OutputCeiling(runtime.Tool, "small_budget"); got != floorDerivedCeiling {
		t.Errorf("scoped small_budget ceiling = %d, want %d - the parent's inflated global must not raise it",
			got, floorDerivedCeiling)
	}
	if got, want := scoped.dispatcher.OutputCeiling(runtime.Tool, "big_budget"), (8<<20)+65536+4096; got != want {
		t.Errorf("scoped big_budget ceiling = %d, want %d", got, want)
	}
	// The nested global cap is inherited from the parent and is never looser.
	if got, want := scoped.dispatcher.Policy().MaxOutputBytes, parent.Policy().MaxOutputBytes; got > want {
		t.Errorf("scoped global cap %d is looser than the parent's %d", got, want)
	}
}

// TestScopedDispatcherWithoutParentDerivesPerToolCeilings pins the
// nil-Dispatcher path. parentPolicy() returns a ZERO runtime.Policy when no
// parent dispatcher is attached, so NewToolDispatcher derives the nested
// global cap from the restricted registry itself. Per-tool ceilings must still
// come from each tool's own budget on that path.
func TestScopedDispatcherWithoutParentDerivesPerToolCeilings(t *testing.T) {
	reg := probeRegistry()
	h := &MultiStepHandler{FullRegistry: reg} // no parent dispatcher
	if got := h.parentPolicy().MaxOutputBytes; got != 0 {
		t.Fatalf("parentPolicy() with no dispatcher returned MaxOutputBytes = %d, want 0", got)
	}
	scoped, err := h.newScopedLoop()
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.dispatcher.Close()

	wantGlobal := (8 << 20) + 65536 + 4096
	if got := scoped.dispatcher.Policy().MaxOutputBytes; got != wantGlobal {
		t.Errorf("scoped global cap = %d, want the registry-derived %d", got, wantGlobal)
	}
	if got := scoped.dispatcher.OutputCeiling(runtime.Tool, "small_budget"); got != floorDerivedCeiling {
		t.Errorf("scoped small_budget ceiling = %d, want %d", got, floorDerivedCeiling)
	}
	if got := scoped.dispatcher.OutputCeiling(runtime.Tool, "big_budget"); got != wantGlobal {
		t.Errorf("scoped big_budget ceiling = %d, want %d", got, wantGlobal)
	}
}
