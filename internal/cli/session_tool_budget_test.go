package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// floorDerivedCeiling mirrors runtime's derivation for a tool that declares no
// result budget: 262144 (historical floor) + 65536 (Policy default
// MaxInputBytes) + 4096 (framing slack).
const floorDerivedCeiling = 262144 + 65536 + 4096

// TestSessionToolCeilingsAreFloorDerived pins the bound the session dispatcher
// enforces on its OWN tools - delegate, dispatch_tasks, the orchestration
// control tools and the execution-history tools. None of them declares a
// tools.ResultBudgetTool, so each is bound at max(nothing, floor) + input
// allowance + slack, capped by the policy.
//
// This is worth pinning because the per-tool derivation deliberately REMOVES
// slack these tools used to get for free. Before it, one global backstop was
// computed as the max over every registered budget, so raising [tools]
// max_read_bytes to 2MiB silently handed dispatch_tasks a 2MiB+ output ceiling
// it never justified - and dispatch_tasks concatenates N sub-agent outputs with
// no byte bound of its own. They are now bound by the same floor-derived value
// they have on the default config, whatever the read budget is set to.
func TestSessionToolCeilingsAreFloorDerived(t *testing.T) {
	cases := map[string]tools.DefaultOptions{
		"defaults":                {},
		"raised max_read_bytes":   {MaxReadBytes: 2 << 20},
		"raised max_output_bytes": {MaxOutputBytes: 1 << 20},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			ws, err := workspace.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			opts.Workspace = ws
			reg := tools.NewDefaultRegistry(opts)
			workspaceTools := map[string]bool{}
			for _, tool := range reg.List() {
				workspaceTools[tool.Name()] = true
			}

			// NewSessionDispatcher adds its own tools to reg as it registers
			// them, so anything in reg afterwards that was not there before is
			// a session tool.
			d, err := newSessionDispatcherMinimal(reg, nullCompleter{}, "test-model", config.DefaultSubagentConfig, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()

			global := d.Policy().MaxOutputBytes
			want := Min(floorDerivedCeiling, global)
			found := 0
			for _, tool := range reg.List() {
				toolName := tool.Name()
				if workspaceTools[toolName] {
					continue // covered by INV-AG-25 in internal/runtime
				}
				found++
				if budgeted, ok := tool.(tools.ResultBudgetTool); ok && budgeted.ResultBudgetBytes() > 0 {
					// A session tool that grows a declared budget is a change
					// of contract, not a drift: assert its own budget clears.
					if got := d.OutputCeiling(runtime.Tool, toolName); got < budgeted.ResultBudgetBytes() {
						t.Errorf("%s: ceiling %d binds below its declared budget %d",
							toolName, got, budgeted.ResultBudgetBytes())
					}
					continue
				}
				if got := d.OutputCeiling(runtime.Tool, toolName); got != want {
					t.Errorf("%s: ceiling = %d, want the floor-derived %d", toolName, got, want)
				}
			}
			if found == 0 {
				t.Fatal("no session tools found; this test is no longer covering anything")
			}
		})
	}
}
