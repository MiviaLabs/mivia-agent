package runtime

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestRunCommandUncappedLargeOutputNotDestroyedByDispatcherCeiling reproduces
// finding F1 (HIGH). With the default config ([tools] max_output_bytes = 0 =
// "uncapped"), the default registry built runCommandTool with maxOut = 0, and
// ResultBudgetBytes() therefore declared NO result budget. The per-tool
// dispatcher ceiling derivation then fell back to its 256KiB floor and added
// the input allowance and slack: floor(262144) + 65536 + 4096 = 331,776.
// run_command output over that ceiling was destroyed WHOLE by the dispatcher
// - it hard-fails, it never truncates or spools, so read_output cannot page
// the bytes back. A command emitting 400,000 bytes got
// {"error":"output budget exceeded",...} in place of its honest result.
//
// The fix makes the uncapped tool declare the same 256 MiB OOM backstop the
// read-class tools use (default_registry.go readClassBudgets): ResultBudgetBytes()
// becomes positive, the derived per-tool ceiling clears honest output, and the
// tool's own dual-capture truncates-with-notice at 256 MiB instead.
func TestRunCommandUncappedLargeOutputNotDestroyedByDispatcherCeiling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell pipeline path")
	}
	ws := regressionWorkspace(t)
	// Default config: MaxOutputBytes left 0 ("uncapped"). RunAllowlist names
	// sh so the tool is registered at all.
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		RunAllowlist: []string{"sh"},
	})
	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)

	res := d.Invoke(context.Background(), Request{
		ID: "run-uncapped", Kind: Tool, Name: "run_command",
		Input: json.RawMessage(`{"argv":["sh","-c","yes x | head -c 400000"]}`),
	})
	body := string(res.Output)
	if res.Err != nil || strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed uncapped run_command output: err=%v body=%q", res.Err, body)
	}
	// The result must carry its honest exit-status header ...
	if !strings.Contains(body, "exit=0") {
		t.Fatalf("output lost its exit=0 header; body=%q", body[:min(len(body), 200)])
	}
	// ... and the honest output itself, not a stub. `yes x | head -c 400000`
	// emits 400,000 bytes of alternating "x\n".
	if !strings.Contains(body, "x\nx") {
		t.Fatalf("honest command output missing; body=%q", body[:min(len(body), 200)])
	}
	if len(body) < 400000 {
		t.Fatalf("output shrunk to %d bytes; the 400,000-byte result must reach the model whole", len(body))
	}
}
