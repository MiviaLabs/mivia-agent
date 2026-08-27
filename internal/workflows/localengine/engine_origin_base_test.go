package localengine_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestEngineAdmissionRecordsOriginBaseCommit mirrors the workspace-level
// TestAdmissionRecordsOriginBaseCommit for the ENGINE admission path: a local
// base branch ahead of origin must record BOTH the local HEAD as BaseCommit
// AND the origin tracking ref as OriginBaseCommit, so delivery step 6b can
// verify the PR base against the remote instead of the local branch. The
// engine previously dropped identity.OriginBaseCommit, leaving engine-admitted
// runs with OriginBaseCommit=” and a false permanent refusal in the
// legitimate local-ahead-of-origin state.
func TestEngineAdmissionRecordsOriginBaseCommit(t *testing.T) {
	repoRoot, _ := newLocalAheadOfOriginRepo(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)
	originCommit := runGitOutT(t, repoRoot, "rev-parse", "refs/remotes/origin/main")
	localCommit := runGitOutT(t, repoRoot, "rev-parse", "HEAD")
	if localCommit == originCommit {
		t.Fatal("precondition: local main must be ahead of origin/main")
	}

	repo := workflowledger.NewMemoryRepository()
	engine := &localengine.Engine{
		WorkspaceRoot: repoRoot,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &localengine.StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
		NewRunID: func() string { return "wfr-origin-base" },
	}
	started, err := engine.Start(context.Background(), workflowledger.StartRequest{
		Workflow: "two-step", Inputs: map[string]any{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.BaseCommit != localCommit {
		t.Fatalf("BaseCommit = %q, want local HEAD %q", run.BaseCommit, localCommit)
	}
	if run.OriginBaseCommit != originCommit {
		t.Fatalf("OriginBaseCommit = %q, want refs/remotes/origin/main %q", run.OriginBaseCommit, originCommit)
	}
}

// TestEngineResumePreservesOriginBaseCommit pins that the engine resume path
// re-applies the run's recorded OriginBaseCommit to the rebuilt controller
// admission. Without it, resume of a run admitted with OriginBaseCommit set
// fails sameAdmission ("already exists with different admission data") because
// the rebuilt admission compares an empty OriginBaseCommit against the stored
// one, and delivery afterwards would fall back to the local BaseCommit pin.
func TestEngineResumePreservesOriginBaseCommit(t *testing.T) {
	repoRoot, _ := newLocalAheadOfOriginRepo(t)
	writeFileT(t, filepath.Join(repoRoot, ".mivia", "workflows", "two-step.toml"), twoStepTOML)
	originCommit := runGitOutT(t, repoRoot, "rev-parse", "refs/remotes/origin/main")

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
		NewRunID: func() string { return "wfr-origin-base-resume" },
	}
	started, err := engine.Start(context.Background(), workflowledger.StartRequest{
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

	resumed, err := engine.Start(context.Background(), workflowledger.StartRequest{
		Resume: true, RunID: started.RunID, Force: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Resumed || resumed.RunID != started.RunID {
		t.Fatalf("resume result = %+v", resumed)
	}
	run, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.OriginBaseCommit != originCommit {
		t.Fatalf("after resume OriginBaseCommit = %q, want %q", run.OriginBaseCommit, originCommit)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, started.RunID); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.GetRun(context.Background(), started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("after resume status = %q, want succeeded", fresh.Status)
	}
}

// newLocalAheadOfOriginRepo builds a main repo on main with a bare origin and
// the base pushed and fetched, then advances the LOCAL branch ahead of the
// pushed origin base: origin/main sits at the base commit, local main at a
// newer commit, and refs/remotes/origin/main is present so admission records
// OriginBaseCommit.
func newLocalAheadOfOriginRepo(t *testing.T) (repoRoot, originURL string) {
	t.Helper()
	repoRoot, originURL = newRealDeliveryRepo(t) // main at base commit, origin pushed
	runGitT(t, repoRoot, "fetch", "origin")      // guarantee refs/remotes/origin/main exists
	writeFileT(t, filepath.Join(repoRoot, "next.txt"), "local ahead\n")
	runGitT(t, repoRoot, "add", "next.txt")
	runGitT(t, repoRoot, "commit", "-m", "local ahead of origin")
	return repoRoot, originURL
}
