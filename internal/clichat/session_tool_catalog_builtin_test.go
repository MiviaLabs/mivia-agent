package clichat

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// findAdvertisedFunction returns the wire entry for one session tool name.
func findAdvertisedFunction(t *testing.T, specs []map[string]any, name string) map[string]any {
	t.Helper()
	for _, spec := range specs {
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] == name {
			return fn
		}
	}
	t.Fatalf("session tool %q not found in %d advertised specs", name, len(specs))
	return nil
}

func dispatchAgentProperty(t *testing.T, fn map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(fn["parameters"])
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	var params struct {
		Properties struct {
			Tasks struct {
				Items struct {
					Properties struct {
						Agent map[string]any `json:"agent"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"tasks"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	return params.Properties.Tasks.Items.Properties.Agent
}

// TestAdvertisedDispatchTasksShipsRosterAtTurnZero pins the D2 seam: the
// ADVERTISED dispatch_tasks schema (what ships on the first request, before
// any load_tools admission) carries the REAL agent enum and roster prose from
// the binding's resolved registry. Kill mutation: stop threading the
// snapshot into the catalog constructor - the enum degrades to empty and
// this fails.
func TestAdvertisedDispatchTasksShipsRosterAtTurnZero(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, warnings, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	specs := advertisedSessionToolSpecs(toolTierPlan{}, reg)
	fn := findAdvertisedFunction(t, specs, "dispatch_tasks")
	agent := dispatchAgentProperty(t, fn)

	enumRaw, ok := agent["enum"]
	if !ok {
		t.Fatalf("advertised agent enum missing at turn zero: %v", agent)
	}
	var enum []string
	raw, _ := json.Marshal(enumRaw)
	if err := json.Unmarshal(raw, &enum); err != nil {
		t.Fatalf("agent enum not a string array: %v", raw)
	}
	if !slices.Equal(enum, []string{"general-purpose"}) {
		t.Fatalf("turn-zero agent enum = %v, want [general-purpose]", enum)
	}
	description := agent["description"].(string)
	if !strings.Contains(description, "Optional") || !strings.Contains(description, "general-purpose") {
		t.Fatalf("turn-zero routing prose must be optional-aware and name the built-in: %q", description)
	}
}

// TestAdvertisedDispatchTasksNilSnapshotDegradedShape pins the regression
// arm: a nil snapshot keeps today's degraded but valid shape (no enum key,
// no always-available claim), so the seam is safe wherever no registry exists.
func TestAdvertisedDispatchTasksNilSnapshotDegradedShape(t *testing.T) {
	specs := advertisedSessionToolSpecs(toolTierPlan{}, nil)
	fn := findAdvertisedFunction(t, specs, "dispatch_tasks")
	agent := dispatchAgentProperty(t, fn)
	if _, found := agent["enum"]; found {
		t.Fatalf("nil snapshot must keep the degraded shape (no enum key): %v", agent)
	}
	if strings.Contains(agent["description"].(string), "always available") {
		t.Fatalf("nil snapshot must not claim the built-in is available: %v", agent)
	}
}

// TestDeferredPlansStillReserveTailSlots pins that load_tools-slot
// accounting is unchanged by the snapshot argument: a deferred plan's tail
// grows by exactly one load_tools entry.
func TestDeferredPlansStillReserveTailSlots(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	reg, _, _, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	inert := advertisedSessionToolSpecs(cliagents.ToolTierPlan{}, reg)
	deferredPlan := cliagents.ToolTierPlan{Tiers: tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}}}
	// Plan.Deferred() gates on Candidates, which a real tier build fills from
	// the deferred split; set it the way PlanToolTiers would.
	deferredPlan.Candidates = append(deferredPlan.Candidates, tools.TierCandidate{Name: "grep"})
	deferred := advertisedSessionToolSpecs(deferredPlan, reg)
	if gotDeferred, gotInert := len(deferred), len(inert); gotDeferred != gotInert+1 {
		t.Fatalf("deferred tail = %d entries, want inert(%d)+load_tools", gotDeferred, gotInert)
	}
}
