package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// INV-AG-25 - every tool in the default registry has an EXPLICIT recorded
// decision about its result size: either it declares tools.ResultBudgetTool,
// or its name appears below with the reason it needs no declaration.
//
// This exists because the defect class it guards keeps returning by way of
// arithmetic. Twice now a reviewer has concluded that list_dir/glob/grep were
// "far under" the 256KiB floor by multiplying a cap by a TYPICAL name or path
// length. Both times the true worst case - a 255-byte name component, a path
// approaching PATH_MAX - was several times the floor, and the dispatcher
// destroyed real results. So the gate below is not an estimate: a tool either
// declares a bound the derivation can read, or a human writes down why it
// does not, and TestWorstCaseWorkspaceToolOutputStaysWithinBudget proves the
// declared bounds hold against adversarial inputs rather than typical ones.

// buildWorstCaseWorkspace lays out the adversarial tree the harness runs
// against: name components at the 255-byte filesystem limit, a directory
// chain driving matched paths toward PATH_MAX, more entries and matches than
// any cap allows, and large existing files for the diff and window paths.
func buildWorstCaseWorkspace(t *testing.T) *workspace.Root {
	t.Helper()
	ws := regressionWorkspace(t)
	const nameLen = 255 // NAME_MAX

	writeMaxNamedFiles := func(dir, stem string, count int, filler byte) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte("NEEDLE " + strings.Repeat(string(filler), 400) + "\n")
		for i := 0; i < count; i++ {
			n := strings.Repeat(stem, nameLen-9) + fmt.Sprintf("%05d.md", i)
			if err := os.WriteFile(filepath.Join(dir, n), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Flat directory of maximum-length names, more than any entry cap allows.
	writeMaxNamedFiles(filepath.Join(ws.Abs, "flat"), "n", 1600, 'x')
	// Chain of maximum-length components: 12 x 256 = 3072 bytes of prefix.
	deep := ws.Abs
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, strings.Repeat("d", nameLen))
	}
	writeMaxNamedFiles(deep, "f", 300, 'y')

	writeGenerated := func(name string, lines int, format string, filler string) {
		t.Helper()
		var b strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, format, i, filler)
		}
		if err := os.WriteFile(filepath.Join(ws.Abs, name), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Large existing files for the overwrite-diff and line-window paths.
	writeGenerated("bulk.txt", 4000, "old line %d %s\n", strings.Repeat("o", 80))
	writeGenerated("wide.txt", 20000, "line %d %s\n", strings.Repeat("w", 60))
	// In-place edit targets. These stay under the harness's max_read_bytes on
	// purpose: the edit tools refuse a file above it (their whole-file read
	// has no other guard), so a file the size of wide.txt would exercise the
	// refusal path rather than the worst-case RESULT this harness measures.
	writeGenerated("edit-one.txt", 4000, "line %d %s\n", strings.Repeat("e", 60))
	writeGenerated("edit-many.txt", 4000, "line %d %s\n", strings.Repeat("m", 60))
	return ws
}

// resultSizeDecision is the recorded reason a default tool declares no result
// budget. bounded=true asserts the output is bounded by a compile-time
// constant well under outputCeilingFloor; bounded=false records a KNOWN gap
// that this test deliberately does not hide.
type resultSizeDecision struct {
	bounded   bool
	rationale string
}

var unbudgetedDefaultTools = map[string]resultSizeDecision{}

// TestEveryDefaultToolHasARecordedResultSizeDecision is the gate: add a tool
// to the default registry and this test fails until the tool declares a
// result budget or its rationale is recorded above. It enumerates the
// registry, so nothing is covered by a list that silently drifts.
func TestEveryDefaultToolHasARecordedResultSizeDecision(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{RunAllowlist: []string{"echo"}, MaxReadBytes: 256 * 1024, MaxOutputBytes: 200_000})
	seen := map[string]bool{}
	for _, tool := range reg.List() {
		name := tool.Name()
		seen[name] = true
		budgeted, ok := tool.(tools.ResultBudgetTool)
		if ok && budgeted.ResultBudgetBytes() > 0 {
			if _, listed := unbudgetedDefaultTools[name]; listed {
				t.Errorf("%s declares a result budget but is still listed in unbudgetedDefaultTools; remove the stale entry", name)
			}
			continue
		}
		decision, listed := unbudgetedDefaultTools[name]
		if !listed {
			t.Errorf("tool %q declares no tools.ResultBudgetBytes() and has no recorded rationale. "+
				"Either declare a budget (see readFileTool) or add an entry to unbudgetedDefaultTools "+
				"explaining what bounds its output. Do NOT reason from typical sizes - the dispatcher "+
				"destroys, rather than truncates, any result over %d bytes.", name, outputCeilingFloor)
			continue
		}
		if decision.rationale == "" {
			t.Errorf("tool %q has an empty rationale", name)
		}
	}
	for name := range unbudgetedDefaultTools {
		if !seen[name] {
			t.Errorf("unbudgetedDefaultTools lists %q, which the default registry does not register", name)
		}
	}
}

// TestDeclaredBudgetsAreCoveredByTheDerivedCeiling: a declared budget is only
// useful if the derivation actually clears it. Every declared budget plus the
// framing terms must sit at or under the ceiling the dispatcher would use.
func TestDeclaredBudgetsAreCoveredByTheDerivedCeiling(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{RunAllowlist: []string{"echo"}, MaxReadBytes: 256 * 1024})
	ceiling := DeriveOutputCeiling(reg, 0)
	for _, tool := range reg.List() {
		budgeted, ok := tool.(tools.ResultBudgetTool)
		if !ok {
			continue
		}
		if budget := budgeted.ResultBudgetBytes(); budget > 0 {
			if budget+inputAllowance+outputCeilingSlack > ceiling {
				t.Errorf("%s budget %d does not fit under derived ceiling %d", tool.Name(), budget, ceiling)
			}
		}
	}
}

// TestWorstCaseWorkspaceToolOutputStaysWithinBudget is the empirical half of
// the invariant. It builds a deliberately adversarial workspace - name
// components at the 255-byte filesystem limit, a directory chain driving
// paths toward PATH_MAX, far more entries and matches than any cap allows,
// and a large existing file to diff against - then runs every workspace-data
// tool through the production dispatcher. Nothing here depends on an estimate
// of a "typical" name or path length: that estimate is exactly what made this
// defect class survive two prior audits.
func TestWorstCaseWorkspaceToolOutputStaysWithinBudget(t *testing.T) {
	ws := buildWorstCaseWorkspace(t)

	// Entry cap raised well past the byte budget: the byte bound, not the
	// count, must be what stops list_dir.
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, MaxListDirEntries: 100000, MaxReadBytes: 512 * 1024})
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	calls := []struct{ name, input string }{
		{"list_dir", `{"path":"flat"}`},
		{"glob", `{"pattern":"**/*.md"}`},
		{"grep", `{"pattern":"NEEDLE"}`},
		{"read_file", `{"path":"wide.txt","offset":1,"limit":100000}`},
		{"write_file", `{"path":"bulk.txt","content":"tiny\n"}`},
		{"search_replace", `{"path":"edit-one.txt","old_string":"line 0 ","new_string":"LINE 0 "}`},
		{"multi_edit", `{"path":"edit-many.txt","edits":[{"old_string":"line 0 ","new_string":"LINE 0 "},{"old_string":"line 1 ","new_string":"LINE 1 "},{"old_string":"m","new_string":"M","replace_all":true}]}`},
	}
	covered := map[string]bool{}
	for _, c := range calls {
		covered[c.name] = true
		assertWorstCaseCallWithinBudget(t, d, reg, c.name, c.input)
	}
	assertWorstCaseCoverage(t, reg, covered)
}

// assertWorstCaseCallWithinBudget runs one worst-case call through the
// production dispatcher and checks it against both bounds that matter: the
// ceiling the dispatcher would enforce, and the tool's own declaration.
func assertWorstCaseCallWithinBudget(t *testing.T, d *Dispatcher, reg *tools.Registry, name, input string) {
	t.Helper()
	// The bound actually enforced for THIS tool, not the policy cap that a
	// generously budgeted sibling can lift far above what this tool gets.
	ceiling := d.OutputCeiling(Tool, name)
	res := d.Invoke(context.Background(), Request{
		ID: "worst-" + name, Kind: Tool, Name: name, Input: json.RawMessage(input),
	})
	body := string(res.Output)
	if strings.Contains(body, "output budget exceeded") {
		t.Errorf("%s: worst-case workspace produced a result the dispatcher destroyed (ceiling %d)", name, ceiling)
		return
	}
	if res.Err != nil {
		t.Errorf("%s: unexpected failure %v (body=%q)", name, res.Err, body[:min(len(body), 160)])
		return
	}
	if len(body) > ceiling {
		t.Errorf("%s: result %d bytes exceeds the dispatcher ceiling %d", name, len(body), ceiling)
	}
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	// A declared budget bounds tool CONTENT; fixed-size framing (read_file's
	// "… lines X–Y" header, a truncation notice) may ride above it, which
	// is exactly what outputCeilingSlack exists to cover. Anything beyond
	// that makes the declaration - and therefore the derived ceiling -
	// wrong. The strict "notice inside the budget" property of the newly
	// budgeted tools is pinned in internal/tools/result_budget_test.go.
	if budgeted, ok := tool.(tools.ResultBudgetTool); ok {
		budget := budgeted.ResultBudgetBytes()
		if budget > 0 && len(body) > budget+outputCeilingSlack {
			t.Errorf("%s: result %d bytes exceeds its OWN declared budget %d plus framing slack %d - the declaration is a lie",
				name, len(body), budget, outputCeilingSlack)
		}
	}
}

// assertWorstCaseCoverage is the bookkeeping half: a workspace-data tool added
// later must either be exercised by the harness or be recorded as out of it.
func assertWorstCaseCoverage(t *testing.T, reg *tools.Registry, covered map[string]bool) {
	t.Helper()
	outOfHarness := map[string]string{
		"run_command":     "result size is set by the allowlisted program, not by workspace data; bounded by max_output_bytes, which it declares",
		"fetch_url":       "remote response; bounded by max_read_bytes, which it declares",
		"search":          "remote response; bounded by max_tavily_response_bytes, which it declares and enforces on the wire read AND on every composed return path - pinned by TestRegression_TavilySearchLargeAnswerReachesModelWhole",
		"extract":         "remote response; bounded by max_tavily_response_bytes, which it declares and enforces on the wire read AND on every composed return path - pinned by TestRegression_TavilyExtractLargePageReachesModelWhole",
		"find_references": "needs a type-checkable module; its self-truncation budget is pinned by TestFindReferencesBudgetClampedToConfiguredCap",
	}
	for _, tool := range reg.List() {
		name := tool.Name()
		if covered[name] {
			continue
		}
		if _, ok := outOfHarness[name]; !ok {
			t.Errorf("tool %q is neither exercised by the worst-case harness nor recorded as out of it; "+
				"add a call above or a reason to outOfHarness", name)
		}
	}
}
