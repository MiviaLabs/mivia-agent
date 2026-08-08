package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type workflowFailureRepository struct {
	workflowledger.Repository
	clearErr     error
	takeoverErr  error
	casErr       error
	getErr       error
	failGetAfter int
	getCalls     int
}

func (r *workflowFailureRepository) ClearRunClaim(ctx context.Context, runID string) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	return r.Repository.ClearRunClaim(ctx, runID)
}

func (r *workflowFailureRepository) TakeoverRunClaim(ctx context.Context, runID, holder string) error {
	if r.takeoverErr != nil {
		return r.takeoverErr
	}
	return r.Repository.TakeoverRunClaim(ctx, runID, holder)
}

func (r *workflowFailureRepository) CompareAndSetRunStatus(ctx context.Context, runID string, version uint64, status workflowledger.RunStatus, finishedAt *time.Time) error {
	if r.casErr != nil {
		return r.casErr
	}
	return r.Repository.CompareAndSetRunStatus(ctx, runID, version, status, finishedAt)
}

func (r *workflowFailureRepository) GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error) {
	r.getCalls++
	if r.getErr != nil && r.getCalls > r.failGetAfter {
		return workflowledger.RunSnapshot{}, r.getErr
	}
	return r.Repository.GetRun(ctx, runID)
}

func TestExecuteWorkflowResumeReturnsMissingRun(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	err := executeWorkflowResume("wfr-missing", root, filepath.Join(root, "config.toml"), false, false, io.Discard, io.Discard)
	if !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("executeWorkflowResume() error = %v, want not found", err)
	}
}

func TestReconcileWorkflowTerminalRepositoryFailures(t *testing.T) {
	tests := []struct {
		name string
		wrap func(workflowledger.Repository) workflowledger.Repository
	}{
		{
			name: "clear claim",
			wrap: func(repo workflowledger.Repository) workflowledger.Repository {
				return &workflowFailureRepository{Repository: repo, clearErr: errors.New("clear failed")}
			},
		},
		{
			name: "status update",
			wrap: func(repo workflowledger.Repository) workflowledger.Repository {
				return &workflowFailureRepository{Repository: repo, casErr: errors.New("CAS failed")}
			},
		},
		{
			name: "status reload",
			wrap: func(repo workflowledger.Repository) workflowledger.Repository {
				return &workflowFailureRepository{
					Repository: repo, getErr: errors.New("reload failed"), failGetAfter: 1,
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, runID := newDerivedTerminalWorkflowRepo(t)
			terminal, err := reconcileWorkflowTerminal(t.Context(), test.wrap(repo), runID, false, io.Discard)
			if err == nil || terminal {
				t.Fatalf("reconcileWorkflowTerminal() = %v, %v; want false and an error", terminal, err)
			}
		})
	}
}

func newDerivedTerminalWorkflowRepo(t *testing.T) (*workflowledger.StorageRepository, string) {
	t.Helper()
	ctx := t.Context()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	runID := "wfr-terminal-" + strings.ReplaceAll(t.Name(), "/", "-")
	run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, ActiveStepID: "one"}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-one", RunID: runID, StepID: "one", AttemptNo: 1}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	outcome := workflowledger.AttemptOutcome{Status: workflowledger.AttemptStatusSucceeded, ToStepID: "success"}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, storedAttempt.Version, outcome); err != nil {
		t.Fatal(err)
	}
	return repo, runID
}

func TestWorkflowExecutionLockInjectedFailures(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "store.db")
	sentinel := errors.New("injected lock failure")
	t.Run("stat", func(t *testing.T) {
		original := workflowExecutionLockStat
		workflowExecutionLockStat = func(*os.File) (os.FileInfo, error) { return nil, sentinel }
		defer func() { workflowExecutionLockStat = original }()
		if _, err := acquireWorkflowExecutionLock(storePath, "wfr-stat"); !errors.Is(err, sentinel) {
			t.Fatalf("stat error = %v", err)
		}
	})
	t.Run("not regular", func(t *testing.T) {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		original := workflowExecutionLockStat
		workflowExecutionLockStat = func(*os.File) (os.FileInfo, error) { return info, nil }
		defer func() { workflowExecutionLockStat = original }()
		if _, err := acquireWorkflowExecutionLock(storePath, "wfr-mode"); err == nil || !strings.Contains(err.Error(), "regular") {
			t.Fatalf("mode error = %v", err)
		}
	})
	t.Run("lock", func(t *testing.T) {
		original := workflowExecutionLockFile
		workflowExecutionLockFile = func(*os.File) (func(), error) { return nil, sentinel }
		defer func() { workflowExecutionLockFile = original }()
		if _, err := acquireWorkflowExecutionLock(storePath, "wfr-lock"); !errors.Is(err, sentinel) {
			t.Fatalf("lock error = %v", err)
		}
	})
}

func TestWorkflowRunRemainingHelperErrors(t *testing.T) {
	if _, err := parseWorkflowInputValue("1 2", "integer"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("multiple JSON value error = %v", err)
	}
	root := t.TempDir()
	if _, err := readWorkflowRef(filepath.Join(root, "missing"), "ref.txt", 10); err == nil {
		t.Fatal("readWorkflowRef() error = nil for a missing root")
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowRef(root, "directory", 10); err == nil {
		t.Fatal("readWorkflowRef() accepted a directory")
	}
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkflowRef(root, "link.txt", 10); err == nil {
		t.Fatal("readWorkflowRef() accepted a symbolic link")
	}
	if err := os.WriteFile(filepath.Join(root, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	step := definition.Step{OutputSchema: "bad.json"}
	if _, _, _, _, err := loadStepReferences(root, step, nil); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid disk schema error = %v", err)
	}
	parentFile := filepath.Join(root, "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.SubagentConfig{StoreBackend: "sqlite", StorePath: filepath.Join(parentFile, "store.db")}
	if store, _, closeFn, err := openWorkflowStore(root, cfg); err == nil {
		closeFn()
		t.Fatalf("openWorkflowStore() opened a path below a file: %v", store)
	}
}
