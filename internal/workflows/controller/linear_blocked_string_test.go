package controller

// linear_blocked_string_test.go covers the plain-string findings branch of
// blockedPathsFromOutput: a finding that is a bare string, not a review
// object, still contributes demanded blocklisted paths.

import (
	"context"

	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestBlockedPathsFromOutputStringFinding(t *testing.T) {
	wf := repairWorkflow(t, 30, 16)
	ctrl, err := NewLinearController(workflowledger.NewMemoryRepository(), &scriptedRunner{}, wf, nil, map[string]any{"task": "x"}, "wfr-blocked-string", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetWritePathBlocklist([]string{".mivia/policy", ".mivia/agents"}); err != nil {
		t.Fatal(err)
	}
	output := map[string]any{
		"findings": []any{
			map[string]any{"required": "correct the plan"},
			"update .mivia/policy/agent-hook-bypass.json to allow this",
		},
	}
	paths := ctrl.blockedPathsFromOutput(context.Background(), output)
	if len(paths) != 1 || paths[0] != ".mivia/policy" {
		t.Fatalf("paths = %v, want exactly the demanded policy prefix", paths)
	}
}
