package localengine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestStartWithEmptyWorkspaceRootFailsClosed pins that Start refuses to admit
// a run when it cannot resolve a real base ref/commit: with WorkspaceRoot ==
// "", ensureRunWorktree and its resolveLocalIdentity fallback both fail, so
// startNew must return an error instead of silently admitting the run with
// the newRunController placeholder Admission{BaseRef:"main",
// BaseCommit:"test-base"} baked into the durable ledger record.
func TestStartWithEmptyWorkspaceRootFailsClosed(t *testing.T) {
	// loadWorkflow discovers workflows under WorkspaceRoot, or "." when it is
	// empty. Run from a directory that has the workflow definition but is NOT
	// a git repository, so loadWorkflow succeeds and the failure under test is
	// specifically the base-identity resolution in startNew, not a missing
	// workflow file.
	root := t.TempDir()
	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(wfRoot, "two-step.toml"), twoStepTOML)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		Repo: repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-no-workspace" },
	}
	_, err = engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"},
	})
	if err == nil {
		t.Fatal("Start with WorkspaceRoot==\"\" admitted a run instead of failing closed")
	}
	if !strings.Contains(err.Error(), "resolve workflow base identity") {
		t.Fatalf("Start error = %v, want a base-identity resolution failure", err)
	}
	if _, getErr := repo.GetRun(context.Background(), "wfr-no-workspace"); !errors.Is(getErr, workflowledger.ErrNotFound) {
		t.Fatalf("GetRun after a failed Start = %v, want ErrNotFound (no placeholder admission persisted)", getErr)
	}
}

// TestStartResumeForceTakesOverLiveClaim pins that Start(Resume, Force=true)
// actually forces: a claim held by a live (non-expired) holder must still be
// taken over by force-resume. Before this fix, the force branch called
// TakeoverExpiredRunClaim - the same conditional-on-expiry primitive the
// non-force branch uses - so force-resume against a live claim silently
// failed to take it over.
func TestStartResumeForceTakesOverLiveClaim(t *testing.T) {
	repoRoot, _ := newRealDeliveryRepo(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)

	repo := workflowledger.NewMemoryRepository()
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{
				Output:     json.RawMessage(`{"ok":true}`),
				BlockUntil: block,
				OnStep: func(controller.AgentStepRequest) {
					select {
					case entered <- struct{}{}:
					default:
					}
				},
			}
		},
		NewRunID: func() string { return "wfr-force-live-claim" },
	}
	started, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("step did not start")
	}
	if err := engine.Interrupt(started.RunID); err != nil {
		t.Fatal(err)
	}
	close(block)

	// Take the run claim with a fresh (non-expired) lease from another holder,
	// simulating a live executor mid-step.
	if err := repo.ClaimRun(context.Background(), started.RunID, "other-live-executor"); err != nil {
		t.Fatalf("seed live claim: %v", err)
	}

	resumed, err := engine.Start(context.Background(), agenttools.StartRequest{
		Resume: true, RunID: started.RunID, Force: true,
	})
	if err != nil {
		t.Fatalf("force-resume against a live claim must take it over, got: %v", err)
	}
	if !resumed.Resumed || resumed.RunID != started.RunID {
		t.Fatalf("resume result = %+v", resumed)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
}
