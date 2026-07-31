package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Audit regression for commit 0f6e524: the session dispatcher used to build
// its runtime.Policy with MaxOutputBytes zero, which resolved to a fixed
// 256KiB ceiling. An operator raising [tools] max_output_bytes above 256KiB
// then had every compliant run_command result silently destroyed with
// "output budget exceeded". The production construction must carry the
// registry-derived ceiling instead, sitting strictly above every configured
// tool budget.
func TestSessionDispatcherCeilingCoversRaisedRunBudget(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:      ws,
		MaxOutputBytes: 1 << 20, // run_command budget raised past the old fixed 256KiB ceiling
	})
	d, err := NewSessionDispatcher(reg, nullCompleter{}, "test-model", config.DefaultSubagentConfig, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	got := d.Policy().MaxOutputBytes
	if want := runtime.DeriveOutputCeiling(reg, d.Policy().MaxInputBytes); got != want {
		t.Fatalf("session dispatcher MaxOutputBytes = %d, want derived %d", got, want)
	}
	if got <= 1<<20 {
		t.Fatalf("session dispatcher ceiling %d does not clear the configured run_command budget %d", got, 1<<20)
	}
}
