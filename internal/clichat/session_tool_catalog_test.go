package clichat

import (
	"fmt"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// sessionToolNames returns the always-advertised catalog names in catalog
// order (DeferredOnly entries excluded).
func sessionToolNames() []string {
	out := make([]string, 0, len(sessionToolCatalog))
	for _, spec := range sessionToolCatalog {
		if spec.DeferredOnly {
			continue
		}
		out = append(out, spec.Name)
	}
	return out
}

// TestSessionToolCatalogMatchesDispatcherRegistrationOrder pins the catalog to
// the dispatcher's actual registration order (delegation, orchestration,
// messaging, ledger in newSessionDispatcherCore). Drift here silently reorders
// the advertised tools[] tail, which invalidates an OpenAI-compatible
// provider's implicit prompt cache for every session.
func TestSessionToolCatalogMatchesDispatcherRegistrationOrder(t *testing.T) {
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{})
	d, err := newSessionDispatcherMinimal(
		reg,
		nullCompleter{},
		"test-model",
		config.SubagentConfig{DefaultTimeout: 60, StoreBackend: "memory"},
		0,
	)
	if err != nil {
		t.Fatalf("newSessionDispatcherMinimal: %v", err)
	}
	t.Cleanup(d.Close)

	names := sessionToolNames()
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	var got []string
	for _, tool := range reg.List() {
		if _, ok := want[tool.Name()]; ok {
			got = append(got, tool.Name())
		}
	}
	if !slices.Equal(got, names) {
		t.Fatalf("registered session tools = %v, want catalog order %v", got, names)
	}
}

// TestSessionToolCatalogHasExactlyOneDeferredOnlyEntry pins the conditional
// member: load_tools is the only session tool gated on the binding deferring
// something, and it must be the last catalog entry (the tail position the
// dispatcher registers it in).
func TestSessionToolCatalogHasExactlyOneDeferredOnlyEntry(t *testing.T) {
	var deferred []string
	for _, spec := range sessionToolCatalog {
		if spec.DeferredOnly {
			deferred = append(deferred, spec.Name)
		}
	}
	if !slices.Equal(deferred, []string{tools.LoadToolsToolName}) {
		t.Fatalf("DeferredOnly entries = %v, want exactly [load_tools]", deferred)
	}
}

func advertisedSpecNames(specs []provider.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			out = append(out, name)
		}
	}
	return out
}

// TestAdvertisedToolSpecsShipsTheSessionToolTail is the regression test for
// the root binding advertising no dispatcher-owned session tools at all: the
// compiled prompt instructs the model to use them (dispatch_tasks,
// spawn_agent, read_output, ledger_read, ...), so a union built from base
// alone left every one of those instructions unfollowable on the turn it was
// read. Every always-on catalog tool must ship, in catalog order, after the
// core block; load_tools ships only when the plan defers something.
func TestAdvertisedToolSpecsShipsTheSessionToolTail(t *testing.T) {
	base := tierRegistry("read_file", "grep")
	plan := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}},
		Candidates: []tools.TierCandidate{{Name: "grep"}},
	}

	specs, dropped := advertisedToolSpecs(base, plan, nil)
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0 under the cap", dropped)
	}
	want := append([]string{"read_file", "grep"}, sessionToolNames()...)
	want = append(want, tools.LoadToolsToolName)
	if names := advertisedSpecNames(specs); !slices.Equal(names, want) {
		t.Fatalf("advertised = %v, want %v", names, want)
	}

	// An inert plan ships the always-on tail but no load_tools.
	inert, dropped := advertisedToolSpecs(base, toolTierPlan{Tiers: tools.Tiers{Core: []string{"read_file", "grep"}}}, nil)
	if dropped != 0 {
		t.Fatalf("inert dropped = %d, want 0", dropped)
	}
	inertNames := advertisedSpecNames(inert)
	if slices.Contains(inertNames, tools.LoadToolsToolName) {
		t.Fatalf("inert plan advertises load_tools: %v", inertNames)
	}
	wantInert := append([]string{"read_file", "grep"}, sessionToolNames()...)
	if !slices.Equal(inertNames, wantInert) {
		t.Fatalf("inert advertised = %v, want %v", inertNames, wantInert)
	}
}

// TestAdvertisedToolSpecsTailRespectsTheCap pins the reserve accounting: the
// tail ships ON TOP of core+deferred, so truncation must budget for it and
// the FINAL array must never exceed tools.MaxAdvertisedTools.
func TestAdvertisedToolSpecsTailRespectsTheCap(t *testing.T) {
	core := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		core = append(core, fmt.Sprintf("tool_%03d", i))
	}
	base := tierRegistry(core...)
	plan := toolTierPlan{Tiers: tools.Tiers{Core: core}}
	specs, dropped := advertisedToolSpecs(base, plan, nil)
	if len(specs) != tools.MaxAdvertisedTools {
		t.Fatalf("advertised %d tools, want exactly the %d cap with the tail reserved for", len(specs), tools.MaxAdvertisedTools)
	}
	tail := sessionToolNames()
	if want := len(core) - (tools.MaxAdvertisedTools - len(tail)); dropped != want {
		t.Fatalf("dropped = %d, want %d", dropped, want)
	}
	names := advertisedSpecNames(specs)
	if !slices.Equal(names[len(specs)-len(tail):], tail) {
		t.Fatalf("tail = %v, want %v in the last %d slots", names[len(specs)-len(tail):], tail, len(tail))
	}
}
