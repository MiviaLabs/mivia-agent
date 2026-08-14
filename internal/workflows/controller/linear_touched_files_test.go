package controller

// implement.touched_files pins a live e2e finding: the review_panel step
// only ever saw the implementing agent's own self-reported files_changed
// (schemas/change-summary-v1.json), never the actual git diff, so an agent
// that silently touched an out-of-scope file was invisible to every panel
// reviewer (confirmed live: a chunk rewrote internal/cli/prompt.go, the
// centralized default agent prompt, while claiming to add unrelated utility
// packages). This binding gives the panel host-measured ground truth
// instead, mirroring checkChunkBaseDrift's pattern.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

func TestContextForStepResolvesTouchedFilesFromRealDiff(t *testing.T) {
	dir := gateGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "unexpected.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wf := stackingGateFixture(t, nil)
	ctrl, err := gateController(t, &scriptedRunner{}, wf, map[string]any{"task": "add mathutil"}, dir)
	if err != nil {
		t.Fatalf("gateController: %v", err)
	}

	step := definition.Step{ID: "review_panel", Context: []definition.ContextBinding{
		{From: "implement.touched_files", As: "touched_files", MaxBytes: 4096, Optional: true},
	}}
	_, evidence, _, err := ctrl.contextForStep(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("contextForStep: %v", err)
	}
	got, _ := evidence["touched_files"].(string)
	if got == "" {
		t.Fatal("evidence[touched_files] = \"\", want the unstaged unexpected.go to appear in the host-measured diff")
	}
	if !strings.Contains(got, "unexpected.go") {
		t.Fatalf("evidence[touched_files] = %q, want it to name unexpected.go", got)
	}
}

func TestContextForStepTouchedFilesEmptyWithoutGitContext(t *testing.T) {
	wf := stackingGateFixture(t, nil)
	ctrl, err := gateController(t, &scriptedRunner{}, wf, map[string]any{"task": "add mathutil"}, "")
	if err != nil {
		t.Fatalf("gateController: %v", err)
	}
	step := definition.Step{ID: "review_panel", Context: []definition.ContextBinding{
		{From: "implement.touched_files", As: "touched_files", MaxBytes: 4096, Optional: true},
	}}
	_, evidence, _, err := ctrl.contextForStep(context.Background(), step, nil)
	if err != nil {
		t.Fatalf("contextForStep: %v", err)
	}
	if got := evidence["touched_files"]; got != "" {
		t.Fatalf("evidence[touched_files] = %v, want \"\" with no git context wired", got)
	}
}
