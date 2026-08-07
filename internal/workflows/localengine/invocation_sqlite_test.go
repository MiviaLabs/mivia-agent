package localengine_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// TestInvocationKeyAdmitsOneRunAcrossSQLiteRepositories simulates separate
// harness processes by using independent repository instances over one SQLite
// file. Both callers pass lookup before either durable admission occurs.
func TestInvocationKeyAdmitsOneRunAcrossSQLiteRepositories(t *testing.T) {
	root := writeTwoStepWorkspace(t)
	path := filepath.Join(t.TempDir(), "workflow.db")
	storeA, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := storage.OpenSQLite(path)
	if err != nil {
		_ = storeA.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close(); _ = storeB.Close() })
	repos := []workflowledger.Repository{
		workflowledger.NewStorageRepository(storeA), workflowledger.NewStorageRepository(storeB),
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	engines := make([]*localengine.Engine, 0, len(repos))
	for _, repo := range repos {
		engines = append(engines, &localengine.Engine{WorkspaceRoot: root, Repo: repo, NewRunner: func() controller.AgentStepRunner {
			ready <- struct{}{}
			<-release
			return invocationCountingRunner{calls: &calls}
		}})
	}
	results := make(chan agenttools.StartResult, len(engines))
	errs := make(chan error, len(engines))
	for _, engine := range engines {
		go func(engine *localengine.Engine) {
			result, startErr := engine.Start(context.Background(), agenttools.StartRequest{
				Workflow: "two-step", Inputs: map[string]any{"task": "same"}, InvocationKey: "same-sqlite-request",
			})
			results <- result
			errs <- startErr
		}(engine)
	}
	<-ready
	<-ready
	close(release)
	var runID string
	for range engines {
		result := <-results
		if startErr := <-errs; startErr != nil {
			t.Fatalf("Start() error = %v", startErr)
		}
		if runID == "" {
			runID = result.RunID
		} else if result.RunID != runID {
			t.Fatalf("run IDs = %q and %q, want one invocation run", runID, result.RunID)
		}
	}
	for _, engine := range engines {
		waitRun(t, engine, runID)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("step calls = %d, want 2 from one two-step workflow", got)
	}
}
