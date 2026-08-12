package runtime

import (
	"os/exec"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// inputAllowance mirrors the Policy default MaxInputBytes used by the
// derivation when the caller passes 0.
const inputAllowance = 64 << 10

func newCeilingRegistry(t *testing.T, opts tools.DefaultOptions) *tools.Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts.Workspace = ws
	return tools.NewDefaultRegistry(opts)
}

// TestDeriveOutputCeilingDefaults pins the derived backstop for the default
// tools config: grep/glob declare a 256 MiB safety backstop (when no
// max_read_bytes is configured), so the ceiling is that plus input
// allowance and slack.
func TestDeriveOutputCeilingDefaults(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{})
	got := DeriveOutputCeiling(reg, 0)
	want := (256 << 20) + inputAllowance + outputCeilingSlack
	if got != want {
		t.Fatalf("DeriveOutputCeiling(defaults) = %d, want %d", got, want)
	}
}

// TestDeriveOutputCeilingRaisedRunBudget: an operator raising run_command's
// max_output_bytes above 256KiB must raise the backstop with it - the audit
// showed 1MB command output being silently destroyed at the old fixed 256KiB.
// When no max_read_bytes is set, grep/glob's 256 MiB safety backstop
// dominates the ceiling.
func TestDeriveOutputCeilingRaisedRunBudget(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{MaxOutputBytes: 1 << 20})
	got := DeriveOutputCeiling(reg, 0)
	want := (256 << 20) + inputAllowance + outputCeilingSlack
	if got != want {
		t.Fatalf("DeriveOutputCeiling(max_output_bytes=1MB) = %d, want %d", got, want)
	}
}

// TestDeriveOutputCeilingRaisedReadBudget: same for max_read_bytes.
func TestDeriveOutputCeilingRaisedReadBudget(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{MaxReadBytes: 2 << 20})
	got := DeriveOutputCeiling(reg, 0)
	if want := (2 << 20) + inputAllowance + outputCeilingSlack; got != want {
		t.Fatalf("DeriveOutputCeiling(max_read_bytes=2MB) = %d, want %d", got, want)
	}
}

// TestDeriveOutputCeilingCoversEveryDeclaredBudget: whatever the config -
// including a large max_tool_result_bytes, which only ever clamps per-tool
// budgets downward - every tool's declared budget plus the framing terms
// must sit at or under the derived ceiling, so no honest output can be
// destroyed.
func TestDeriveOutputCeilingCoversEveryDeclaredBudget(t *testing.T) {
	cases := map[string]tools.DefaultOptions{
		"defaults":                    {},
		"large max_tool_result_bytes": {MaxToolResultBytes: 8 << 20},
		"small max_tool_result_bytes": {MaxToolResultBytes: 4096},
		"raised everything":           {MaxReadBytes: 1 << 20, MaxOutputBytes: 3 << 20, MaxToolResultBytes: 4 << 20},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			reg := newCeilingRegistry(t, opts)
			ceiling := DeriveOutputCeiling(reg, 0)
			for _, tool := range reg.List() {
				budgeted, ok := tool.(tools.ResultBudgetTool)
				if !ok {
					continue
				}
				budget := budgeted.ResultBudgetBytes()
				if budget <= 0 {
					continue
				}
				if budget+inputAllowance+outputCeilingSlack > ceiling {
					t.Errorf("tool %q budget %d + allowance + slack exceeds ceiling %d",
						tool.Name(), budget, ceiling)
				}
			}
		})
	}
}

// TestDeriveOutputCeilingCoversDiagnosticsCommands: with a configured
// diagnostics_commands map whose default entry's argv[0] is on the run_command
// allowlist, get_diagnostics registers and declares its 256 KiB result budget
// (diagnosticsDefaultBudget - exactly the derivation floor). The derived
// ceiling must cover that declared budget plus the input allowance (results
// may echo request input verbatim) plus framing slack - with the default
// input cap and with an explicit MaxInputBytes. And because the budget equals
// the floor, the tool must never raise the shared global cap: the same config
// without the diagnostics commands derives an identical ceiling, and the
// tool's per-tool ceiling stays under the dispatcher's global one.
func TestDeriveOutputCeilingCoversDiagnosticsCommands(t *testing.T) {
	// Registration resolves argv[0] on PATH (resolveAllowedCommand), so skip
	// where no sh exists rather than fail the whole package run (mirrors
	// requirePOSIXDiagnostics in internal/tools).
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("get_diagnostics registration requires sh on PATH")
	}
	opts := tools.DefaultOptions{
		RunAllowlist: []string{"sh"},
		DiagnosticsCommands: map[string][]string{
			"default": {"sh", "-c", "true"},
		},
	}
	reg := newCeilingRegistry(t, opts)

	tool, ok := reg.Get(tools.GetDiagnosticsToolName)
	if !ok {
		t.Fatal("get_diagnostics not registered with DiagnosticsCommands configured and the default argv[0] allowlisted")
	}
	budgeted, ok := tool.(tools.ResultBudgetTool)
	if !ok {
		t.Fatalf("get_diagnostics does not implement tools.ResultBudgetTool")
	}
	budget := budgeted.ResultBudgetBytes()
	if budget != 256<<10 {
		t.Errorf("get_diagnostics ResultBudgetBytes = %d, want the declared 256 KiB", budget)
	}

	// Coverage with the default input allowance (DeriveOutputCeiling's <=0
	// argument) and with an explicit MaxInputBytes: budget + input + slack
	// must sit at or under the derived ceiling.
	ceiling := DeriveOutputCeiling(reg, 0)
	if budget+inputAllowance+outputCeilingSlack > ceiling {
		t.Errorf("get_diagnostics budget %d + allowance %d + slack %d exceeds ceiling %d",
			budget, inputAllowance, outputCeilingSlack, ceiling)
	}
	explicit := 128 << 10
	if got := DeriveOutputCeiling(reg, explicit); budget+explicit+outputCeilingSlack > got {
		t.Errorf("get_diagnostics budget %d + explicit MaxInputBytes %d + slack %d exceeds ceiling %d",
			budget, explicit, outputCeilingSlack, got)
	}

	// Never raise the global cap: the 256 KiB budget equals the derivation
	// floor, so the same config without the diagnostics command must derive
	// an identical ceiling, and the dispatcher's per-tool ceiling for the
	// tool must stay at or under that global value.
	plain := newCeilingRegistry(t, tools.DefaultOptions{RunAllowlist: []string{"sh"}})
	if got := DeriveOutputCeiling(plain, 0); got != ceiling {
		t.Errorf("ceiling with get_diagnostics = %d, without = %d; the tool must never raise the global cap",
			ceiling, got)
	}
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Policy().MaxOutputBytes; got != ceiling {
		t.Errorf("dispatcher global cap = %d, want derived %d", got, ceiling)
	}
	if got := d.OutputCeiling(Tool, tools.GetDiagnosticsToolName); got > ceiling {
		t.Errorf("get_diagnostics per-tool ceiling %d exceeds the global cap %d", got, ceiling)
	}
}

// TestDeriveOutputCeilingFloor: with no declared budgets (or no registry at
// all) the runaway backstop keeps its historical 256KiB floor.
func TestDeriveOutputCeilingFloor(t *testing.T) {
	if got := DeriveOutputCeiling(tools.NewRegistry(), 0); got != 256<<10 {
		t.Fatalf("DeriveOutputCeiling(empty) = %d, want %d", got, 256<<10)
	}
	if got := DeriveOutputCeiling(nil, 0); got != 256<<10 {
		t.Fatalf("DeriveOutputCeiling(nil) = %d, want %d", got, 256<<10)
	}
}

// TestNewToolDispatcherDerivesCeiling: a zero Policy.MaxOutputBytes - the
// production construction in cli/dispatcher.go and the agent loop's fallback
// path - must yield the derived ceiling, not the raw 256KiB default.
func TestNewToolDispatcherDerivesCeiling(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{MaxOutputBytes: 1 << 20})
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got, want := d.Policy().MaxOutputBytes, DeriveOutputCeiling(reg, 0); got != want {
		t.Fatalf("dispatcher MaxOutputBytes = %d, want derived %d", got, want)
	}
}

// TestNewToolDispatcherKeepsExplicitCeiling: an explicit policy value is a
// deliberate bound (tests and callers rely on it) and must not be re-derived.
func TestNewToolDispatcherKeepsExplicitCeiling(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{})
	d, err := NewToolDispatcher(reg, Policy{MaxOutputBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Policy().MaxOutputBytes; got != 512 {
		t.Fatalf("dispatcher MaxOutputBytes = %d, want explicit 512", got)
	}
}

// TestBudgetedToolsDoNotDeclareLoopTruncationBound pins that read_file and
// run_command do NOT declare Capability.MaxResultBytes. The agent loop uses
// that field as a wire truncation bound, and both tools emit honest framing
// (window header/notice, argv echo header) ON TOP of their content budget -
// declaring the budget there would make the loop tail-cut the framing the
// tools construct to stay honest. Their budgets reach the dispatcher
// backstop via tools.ResultBudgetTool instead.
func TestBudgetedToolsDoNotDeclareLoopTruncationBound(t *testing.T) {
	reg := newCeilingRegistry(t, tools.DefaultOptions{RunAllowlist: []string{"echo"}})
	for _, name := range []string{"read_file", "run_command"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if _, ok := tool.(tools.ResultBudgetTool); !ok {
			t.Errorf("%s does not implement tools.ResultBudgetTool", name)
		}
		capable, ok := tool.(tools.CapableTool)
		if !ok {
			t.Fatalf("%s does not implement tools.CapableTool", name)
		}
		if got := capable.Capability(nil).MaxResultBytes; got != 0 {
			t.Errorf("%s declares Capability.MaxResultBytes = %d; the loop would tail-cut its honest framing", name, got)
		}
	}
}
