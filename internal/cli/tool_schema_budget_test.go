package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Core tool schema token budget (plan 81, corrected numbers).
//
// The core tier ships full JSON schemas on EVERY request; it is the largest
// fixed per-request prompt cost. These tests pin that cost with the repo's
// own estimator (provider.EstimateToolSchemaCost, the same cost model the
// context planner uses) so the budget cannot silently grow. The fixture below
// pins every conditional registration input (Tavily key, run allowlist,
// memory store, workflows dir, diagnostics commands), so the measurement is
// identical on any machine and never depends on the environment.
//
// Ratchet record (schema tokens, len/4 heuristic + 4 frame tokens per tool):
//
//	baseline 2026-08-16: core 4629, advertised 10129
//	  (deferred 1334 across 10 tools, session tail 4166 across 13)
//	tightening 2026-08-16: core 3851, advertised 9351 (-778 tok, -16.8% on
//	  core; Description()/parameter text of all 19 core tools compressed,
//	  semantics and pinned phrases kept); budgets set at +5% margin
//
// Budget constants are ratcheted down only with a measured reason recorded
// here and in the commit message.
const (
	coreSchemaTokenBudget       = 4043 // achieved 3851 + 5% margin
	advertisedSchemaTokenBudget = 9818 // achieved 9351 + 5% margin
)

// pinnedBudgetRegistry builds the default registry with every conditional
// input fixed, mirroring this repo's own live surface (.mivia/mivia.toml):
// extract (Tavily key), run_command (allowlist), memory tools (store),
// workflow tools (.mivia/workflows/ + the builder this package's init
// installs), code-nav tools (workspace), get_diagnostics (configured default
// whose argv[0] is allowlisted and on PATH wherever `go test` runs).
func pinnedBudgetRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(memory.Config{Backend: memory.BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
		RunAllowlist: []string{"go", "make", "python3", "echo"},
		Memory:       store,
		DiagnosticsCommands: map[string][]string{
			"default": {"go", "vet", "./..."},
		},
	})
}

// loadRepoCoreTier reads [tools] core from this repo's own .mivia/mivia.toml,
// so the budget pins the repo's configured core tier and drifts loudly when
// the config changes instead of duplicating nineteen names in Go.
func loadRepoCoreTier(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", ".mivia", "mivia.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repo config %s: %v", path, err)
	}
	var parsed struct {
		Tools struct {
			Core []string `toml:"core"`
		} `toml:"tools"`
	}
	if err := toml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(parsed.Tools.Core) == 0 {
		t.Fatalf("%s declares no [tools] core tier", path)
	}
	return parsed.Tools.Core
}

// budgetSpecs splits the advertised wire array for the pinned fixture into
// core, deferred (one-line description, full parameters), and session-tail
// specs, in advertised order.
func budgetSpecs(t *testing.T) (core, deferred, session []provider.ToolSpec) {
	t.Helper()
	base := pinnedBudgetRegistry(t)
	coreNames := loadRepoCoreTier(t)
	res := &config.Resolved{Tools: config.ToolsConfig{Core: &coreNames}}
	plan := planToolTiers(base, nil, res)
	advertised, dropped := advertisedToolSpecs(base, plan)
	if dropped != 0 {
		t.Fatalf("advertisedToolSpecs dropped %d tools", dropped)
	}
	deferredSet := make(map[string]struct{}, len(plan.Tiers.Deferred))
	for _, name := range plan.Tiers.Deferred {
		deferredSet[name] = struct{}{}
	}
	coreSet := make(map[string]struct{}, len(plan.Tiers.Core))
	for _, name := range plan.Tiers.Core {
		coreSet[name] = struct{}{}
	}
	for _, spec := range advertised {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if _, ok := coreSet[name]; ok {
			core = append(core, spec)
			continue
		}
		if _, ok := deferredSet[name]; ok {
			deferred = append(deferred, spec)
			continue
		}
		session = append(session, spec)
	}
	return core, deferred, session
}

// specCostPrices one spec the way EstimateToolSchemaCost does, and splits its
// marshaled bytes into description vs parameters shares.
func specCost(t *testing.T, spec provider.ToolSpec) (tokens, descBytes, paramBytes int) {
	t.Helper()
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	tokens = 4 + len(encoded)/4
	fn, _ := spec["function"].(map[string]any)
	if raw, err := json.Marshal(fn["description"]); err == nil {
		descBytes = len(raw)
	}
	if raw, err := json.Marshal(fn["parameters"]); err == nil {
		paramBytes = len(raw)
	}
	return tokens, descBytes, paramBytes
}

// TestCoreToolSchemaBudget is the growth guard: the core tier must stay at
// the configured size and at or under the ratcheted token budgets. A failure
// here means a schema grew (or a tool joined/left the tier) without a
// measured justification - raise the constants only with the measurement
// recorded in the ratchet table above.
func TestCoreToolSchemaBudget(t *testing.T) {
	core, deferred, session := budgetSpecs(t)
	if len(core) != 19 {
		t.Fatalf("core tier = %d tools, want 19 (repo [tools] core)", len(core))
	}
	coreCost, err := provider.EstimateToolSchemaCost(core)
	if err != nil {
		t.Fatal(err)
	}
	deferredCost, err := provider.EstimateToolSchemaCost(deferred)
	if err != nil {
		t.Fatal(err)
	}
	sessionCost, err := provider.EstimateToolSchemaCost(session)
	if err != nil {
		t.Fatal(err)
	}
	total := coreCost + deferredCost + sessionCost
	t.Logf("core %d tools = %d tok; deferred %d = %d tok; session tail %d = %d tok; advertised total = %d tok",
		len(core), coreCost, len(deferred), deferredCost, len(session), sessionCost, total)
	if coreCost > coreSchemaTokenBudget {
		t.Fatalf("core schema cost = %d tok, budget %d; tighten schema text or ratchet with a measured reason", coreCost, coreSchemaTokenBudget)
	}
	if total > advertisedSchemaTokenBudget {
		t.Fatalf("advertised schema cost = %d tok, budget %d; tighten schema text or ratchet with a measured reason", total, advertisedSchemaTokenBudget)
	}
}

// TestCoreToolSchemaCostBreakdown is the measurement surface behind the
// budget: `go test -v` prints the per-tool table (cost, description-byte
// share, parameter-byte share) used to pick tightening targets and to fill
// the ratchet record. Parameter JSON dominates schema bytes, so the table
// sorts by cost descending.
func TestCoreToolSchemaCostBreakdown(t *testing.T) {
	core, _, _ := budgetSpecs(t)
	type row struct {
		name       string
		tokens     int
		descBytes  int
		paramBytes int
	}
	rows := make([]row, 0, len(core))
	totalTokens, totalDesc, totalParams := 0, 0, 0
	for _, spec := range core {
		fn, _ := spec["function"].(map[string]any)
		name, _ := fn["name"].(string)
		tokens, descBytes, paramBytes := specCost(t, spec)
		rows = append(rows, row{name, tokens, descBytes, paramBytes})
		totalTokens += tokens
		totalDesc += descBytes
		totalParams += paramBytes
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].tokens > rows[j].tokens })
	for _, r := range rows {
		t.Logf("%-20s %5d tok  desc %5d B  params %5d B", r.name, r.tokens, r.descBytes, r.paramBytes)
	}
	t.Logf("%-20s %5d tok  desc %5d B (%.1f%%)  params %5d B (%.1f%%)",
		"TOTAL", totalTokens, totalDesc,
		100*float64(totalDesc)/float64(totalDesc+totalParams),
		totalParams, 100*float64(totalParams)/float64(totalDesc+totalParams))
}
