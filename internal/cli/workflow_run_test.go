package cli

// Fail-settle regression for `mivia workflow run`: when the post-plan stack
// drive hits a terminally failed chunk, the plan run must be fail-settled
// instead of staying parked at delivery_pending.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// TestWorkflowRunFailSettlesPlanRunFailed proves that executeWorkflowRun
// fail-settles a delivery_pending plan run when maybeDriveSettledStack sees a
// terminally failed chunk. The drive stub injects the durable failure so the
// test does not depend on a full chunk controller run.
func TestWorkflowRunFailSettlesPlanRunFailed(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled := compileWorkflowFile(t, miniStackPath)
	rawDefinition, err := os.ReadFile(miniStackPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := miniStackSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	var runID string
	originalBuild := workflowRunBuild
	t.Cleanup(func() { workflowRunBuild = originalBuild })
	workflowRunBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, _ *compiler.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, id string, _ *workflowledger.Snapshot, _ []byte, _ *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry) (workflowControllerBuild, error) {
		runID = id
		synth, err := compiler.SynthesizeStacking(compiled)
		if err != nil {
			return workflowControllerBuild{}, err
		}
		steps := scriptedMiniStackRuntimes(t, synth)
		ctrl, err := controller.NewLinearController(repo, scriptedStackRunner{}, synth, steps, map[string]any{"task": "x"}, id, rawSnapshot)
		if err != nil {
			return workflowControllerBuild{}, err
		}
		return workflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(map[string]string{"task": "x"})},
		}, nil
	}

	originalDrive := workflowStackDriveToCompletion
	t.Cleanup(func() { workflowStackDriveToCompletion = originalDrive })
	workflowStackDriveToCompletion = func(_ context.Context, _ *preparedWorkflowRun, ledger *tasks.Store, stackID string, _ []ChunkPlan, _ bool, _ string, _ map[string]string, _ bool, _, _ io.Writer) error {
		_ = ledger.TransitionTask(stackID, "c1", stackStatusFailed)
		return errors.New("stack drive: chunk c1 failed terminally")
	}

	var stdout bytes.Buffer
	err = executeWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"}, false, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot complete") {
		t.Fatalf("executeWorkflowRun() error = %v; stdout = %q", err, stdout.String())
	}
	if runID == "" {
		t.Fatal("runID not captured from workflowRunBuild")
	}

	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := workflowledger.NewStorageRepository(store).GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusFailed {
		t.Fatalf("plan run status = %q, want failed after fail-settle", run.Status)
	}
}

// TestWorkflowRunSettleFailurePropagates pins executeWorkflowRun's
// settleErr != nil branch: when maybeDriveSettledStack fails AND the
// fail-settle attempt itself errors (a transient store fault during the
// CAS), the settle error must surface wrapped rather than being silently
// swallowed or reported as the original drive error.
func TestWorkflowRunSettleFailurePropagates(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	miniStackPath := filepath.Join(root, ".mivia", "workflows", "mini-stack.toml")
	if err := os.WriteFile(miniStackPath, []byte(miniStackWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled := compileWorkflowFile(t, miniStackPath)
	rawDefinition, err := os.ReadFile(miniStackPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := miniStackSnapshot(t, root, compiled, rawDefinition)
	rawSnapshot, err := workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	originalBuild := workflowRunBuild
	t.Cleanup(func() { workflowRunBuild = originalBuild })
	workflowRunBuild = func(_ string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, _ *compiler.CompiledWorkflow, _ string, _ map[string]any, _ map[string]string, _ []byte, id string, _ *workflowledger.Snapshot, _ []byte, _ *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry) (workflowControllerBuild, error) {
		synth, err := compiler.SynthesizeStacking(compiled)
		if err != nil {
			return workflowControllerBuild{}, err
		}
		steps := scriptedMiniStackRuntimes(t, synth)
		ctrl, err := controller.NewLinearController(repo, scriptedStackRunner{}, synth, steps, map[string]any{"task": "x"}, id, rawSnapshot)
		if err != nil {
			return workflowControllerBuild{}, err
		}
		return workflowControllerBuild{
			Controller: ctrl,
			Dispatcher: workflowTestDispatcher{},
			Admission:  controller.Admission{InputDigest: workflowledger.InputDigest(map[string]string{"task": "x"})},
		}, nil
	}

	originalDrive := workflowStackDriveToCompletion
	t.Cleanup(func() { workflowStackDriveToCompletion = originalDrive })
	workflowStackDriveToCompletion = func(_ context.Context, _ *preparedWorkflowRun, ledger *tasks.Store, stackID string, _ []ChunkPlan, _ bool, _ string, _ map[string]string, _ bool, _, _ io.Writer) error {
		_ = ledger.TransitionTask(stackID, "c1", stackStatusFailed)
		return errors.New("stack drive: chunk c1 failed terminally")
	}

	originalSettle := settleFailedStackPlanRunIfNeededFn
	t.Cleanup(func() { settleFailedStackPlanRunIfNeededFn = originalSettle })
	settleFailedStackPlanRunIfNeededFn = func(context.Context, *preparedWorkflowRun, string, string) (bool, error) {
		return false, errors.New("store fault during fail-settle CAS")
	}

	var stdout bytes.Buffer
	err = executeWorkflowRun("mini-stack", root, filepath.Join(root, "config.toml"), []string{"task=x"}, false, &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "settle failed plan run") {
		t.Fatalf("executeWorkflowRun() error = %v, want it to surface the settle failure; stdout = %q", err, stdout.String())
	}
}
