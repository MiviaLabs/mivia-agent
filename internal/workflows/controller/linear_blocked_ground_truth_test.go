package controller

// TestBlockedGroundTruthCatchesUndisclosedWrite pins a live-audit finding:
// the write-blocklist gate previously trusted ONLY the implementing agent's
// self-reported blocked_paths/files_changed. An agent that writes a
// blocklisted path and simply omits it from its own report (honestly or
// not) bypassed the gate entirely - the same self-report gap already fixed
// for review visibility (touchedFilesEvidence), but here it is the actual
// enforcement mechanism for a hard security boundary. blockedPathsFromOutput
// now also cross-checks the host-measured actual diff.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestBlockedGroundTruthCatchesUndisclosedWrite(t *testing.T) {
	dir := gateGitRepo(t)
	base := gitHead(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".mivia", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The agent actually wrote a blocklisted file...
	if err := os.WriteFile(filepath.Join(dir, ".mivia", "workflows", "bug-fix.toml"), []byte("max_bytes = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ...but its own self-report only admits the harmless file.
	runner := &scriptedRunner{outputsByStepCall: map[string]json.RawMessage{
		"implement#1": json.RawMessage(`{"summary":"implemented","files_changed":["a.go"],"inspected":["a.go"],"addressed_findings":[],"pr_title":"fix","pr_summary":"fixes the bug"}`),
	}}
	wf := repairWorkflow(t, 30, 16)
	repo := workflowledger.NewMemoryRepository()
	ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{
		"implement": {Agent: agents.ResolvedAgent{Name: "dev"}},
		"review":    {Agent: agents.ResolvedAgent{Name: "rev"}},
	}, map[string]any{"task": "x"}, "wfr-blocked-groundtruth", []byte("snap"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetWritePathBlocklist([]string{".mivia/workflows", ".mivia/policy"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetAdmission(Admission{BaseRef: "main", BaseCommit: base, WorktreeName: "workflow-blocked-gt", InputDigest: "d"}); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetGitContext(delivery.GitContext{Dir: dir, GitDir: filepath.Join(dir, ".git")}); err != nil {
		t.Fatal(err)
	}

	got, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "workflow blocked") || !strings.Contains(err.Error(), ".mivia/workflows/bug-fix.toml") {
		t.Fatalf("run error = %v, want a blocked cause naming the undisclosed write .mivia/workflows/bug-fix.toml", err)
	}
	if got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("run status = %v, want failed", got.Status)
	}
}
