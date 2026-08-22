package cliagents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// longDescTool is a test-only tool with a genuinely long, multi-sentence
// description, so shortDescTool's shrink is actually observable (namedTool's
// single-word description has no sentence to cut).
type longDescTool struct{ namedTool }

func (t longDescTool) Description() string {
	return "Do the thing this tool does. It takes several parameters and has a number of edge cases worth describing at length here."
}

func TestAdvertisedToolSpecsShortensADeferredDescription(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(namedTool{name: "read_file"})
	base.Register(longDescTool{namedTool{name: "grep"}})
	plan := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}},
		Candidates: []tools.TierCandidate{{Name: "grep", Description: longDescTool{}.Description()}},
	}

	specs, _ := advertisedToolSpecs(base, plan)
	byName := make(map[string]map[string]any, len(specs))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		byName[name] = fn
	}

	core, deferred := byName["read_file"], byName["grep"]
	if core == nil || deferred == nil {
		t.Fatalf("advertised = %v, want both read_file and grep", byName)
	}
	if core["description"] != "read_file" {
		t.Fatalf("core description changed: %q", core["description"])
	}
	wantShort := tools.FirstLine(longDescTool{}.Description())
	if deferred["description"] != wantShort {
		t.Fatalf("deferred description = %q, want the one-liner %q", deferred["description"], wantShort)
	}
	if deferred["description"] == (longDescTool{}).Description() {
		t.Fatal("deferred description was not shortened at all")
	}
	if _, ok := deferred["parameters"].(map[string]any); !ok {
		t.Fatalf("deferred parameters missing or wrong shape: %v", deferred["parameters"])
	}
}

// TestAdvertisedToolSpecsShortensRealToolDescriptionsIntact closes the gap
// left by TestAdvertisedToolSpecsShortensADeferredDescription's synthetic
// fixture: that test's longDescTool has no quotes, parens, or abbreviations,
// so it could never exercise the exact bug class this repo has hit before -
// TestDeferredIndexRendersEveryShippedToolIntact (internal/tools) documents
// that cutting at the first period once truncated list_dir's own quoted
// default, leaving an unbalanced quote. That test only walks
// tools.DeferredIndex; it never exercises shortDescTool, the new call site
// this package added. This test walks the same real default registry through
// advertisedToolSpecs instead, so a regression in firstLine that somehow
// broke only the shortDescTool call shape (identical input today, but not
// enforced by any test) would be caught here even though it would slip past
// the internal/tools test.
func TestAdvertisedToolSpecsShortensRealToolDescriptionsIntact(t *testing.T) {
	dir := t.TempDir()
	// find_references registers only when a workspace looks like a project.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
		RunAllowlist: []string{"echo"},
	})
	listed := base.List()
	if len(listed) == 0 {
		t.Fatal("default registry registered no tools")
	}
	var deferredNames []string
	for _, tool := range listed {
		deferredNames = append(deferredNames, tool.Name())
	}
	plan := toolTierPlan{Tiers: tools.Tiers{Deferred: deferredNames}}

	specs, dropped := advertisedToolSpecs(base, plan)
	if dropped != 0 {
		t.Fatalf("dropped = %d, want every real tool advertised", dropped)
	}
	seen := make(map[string]bool, len(listed))
	for _, spec := range specs {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		tool, ok := base.Get(name)
		if !ok {
			continue // session-tool tail, not one of the deferred candidates checked here
		}
		seen[name] = true
		got, _ := fn["description"].(string)
		want := tools.FirstLine(tool.Description())
		if got != want {
			t.Fatalf("%s advertised description = %q, want the one-liner %q", name, got, want)
		}
		if got == "" {
			t.Fatalf("%s rendered with no description", name)
		}
		if strings.Count(got, `"`)%2 != 0 {
			t.Fatalf("%s advertised description has an unbalanced quote: %q", name, got)
		}
		if strings.Count(got, "(") != strings.Count(got, ")") {
			t.Fatalf("%s advertised description has unbalanced parentheses: %q", name, got)
		}
	}
	for _, tool := range listed {
		if !seen[tool.Name()] {
			t.Fatalf("%s never appeared in the advertised specs", tool.Name())
		}
	}
}

// TestAdvertisedToolSpecsIsDeterministic guards the "computed once, frozen
// forever" invariant: calling advertisedToolSpecs twice with the same plan
// must produce byte-identical output, including the shortened description.
func TestAdvertisedToolSpecsIsDeterministic(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(namedTool{name: "read_file"})
	base.Register(longDescTool{namedTool{name: "grep"}})
	plan := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}},
		Candidates: []tools.TierCandidate{{Name: "grep", Description: longDescTool{}.Description()}},
	}

	first, _ := advertisedToolSpecs(base, plan)
	second, _ := advertisedToolSpecs(base, plan)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("advertisedToolSpecs is not deterministic:\n%s\n---\n%s", firstJSON, secondJSON)
	}
}

// TestMeasureSchemaMassLockedTokensPricesTheShortenedDescription guards
// LockedTokens against drifting from what advertisedToolSpecs actually ships:
// it must price the one-line description, not the full raw one, or it
// overstates the real cost of a locked tool once shortDescTool is applied.
func TestMeasureSchemaMassLockedTokensPricesTheShortenedDescription(t *testing.T) {
	base := tools.NewRegistry()
	base.Register(namedTool{name: "read_file"})
	base.Register(longDescTool{namedTool{name: "grep"}})
	plan := toolTierPlan{
		Tiers:      tools.Tiers{Core: []string{"read_file"}, Deferred: []string{"grep"}},
		Candidates: []tools.TierCandidate{{Name: "grep", Description: longDescTool{}.Description()}},
	}
	advertised, _ := advertisedToolSpecs(base, plan)

	mass := measureSchemaMass(advertised, base, plan, nil, "reader", "attach")
	fullCost, err := provider.EstimateToolSchemaCost([]provider.ToolSpec{{
		"type": "function",
		"function": map[string]any{
			"name":        "grep",
			"description": longDescTool{}.Description(),
			"parameters":  map[string]any{"type": "object"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if mass.LockedTokens >= fullCost {
		t.Fatalf("LockedTokens = %d, want fewer tokens than the full-description cost %d", mass.LockedTokens, fullCost)
	}
}
