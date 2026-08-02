package runtime

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestUncappedRunCommandMultiMBSurvivesDispatcher is the production-path gate
// for plan 48 success criterion 2: under default uncapped max_output_bytes=0,
// an honest multi-MB run_command result must pass NewToolDispatcher.Invoke
// without destroy-at-ceiling or tail-truncation.
func TestUncappedRunCommandMultiMBSurvivesDispatcher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dd path")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace: ws, RunAllowlist: []string{"sh", "dd", "tr"},
		RunTimeoutSec: 30, MaxOutputBytes: 0,
	})
	assertUncappedRunBudget(t, reg)

	d, err := NewToolDispatcher(reg, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const wantBody = 2 << 20 // 2 MiB
	ceiling := d.OutputCeiling(Tool, "run_command")
	if ceiling < wantBody {
		t.Fatalf("run_command ceiling = %d, want ≥ %d", ceiling, wantBody)
	}
	input, _ := json.Marshal(map[string]any{
		"argv": []string{"sh", "-c", "dd if=/dev/zero bs=1024 count=2048 2>/dev/null | tr '\\0' A"},
	})
	res := d.Invoke(context.Background(), Request{
		ID: "uncapped-multimb", Kind: Tool, Name: "run_command", Input: input,
	})
	assertMultiMBSurvived(t, res, wantBody, ceiling)
}

func assertUncappedRunBudget(t *testing.T, reg *tools.Registry) {
	t.Helper()
	tool, ok := reg.Get("run_command")
	if !ok {
		t.Fatal("run_command not registered")
	}
	budgeted, ok := tool.(tools.ResultBudgetTool)
	if !ok {
		t.Fatal("run_command must declare ResultBudgetTool")
	}
	if budgeted.ResultBudgetBytes() < 2<<20 {
		t.Fatalf("uncapped budget = %d, want ≥ memory backstop", budgeted.ResultBudgetBytes())
	}
}

func assertMultiMBSurvived(t *testing.T, res Result, wantBody, ceiling int) {
	t.Helper()
	body := string(res.Output)
	if res.Err != nil {
		t.Fatalf("invoke error: %v body=%q", res.Err, body[:min(len(body), 200)])
	}
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("destroyed multi-MB result: %s", body[:min(len(body), 240)])
	}
	if strings.Contains(body, "truncated: kept") {
		t.Fatalf("truncated multi-MB under pass-through: %q", body[max(0, len(body)-120):])
	}
	if !strings.Contains(body, strings.Repeat("A", 1024)) {
		t.Fatalf("missing payload; len=%d head=%q", len(body), body[:min(len(body), 120)])
	}
	if len(body) < wantBody {
		t.Fatalf("result len=%d, want ≥ %d", len(body), wantBody)
	}
	if len(body) > ceiling {
		t.Fatalf("result len=%d exceeds ceiling %d", len(body), ceiling)
	}
}
