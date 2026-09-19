package agenttools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failingRepo forces individual ledger reads to fail so the Service read
// tools' error paths are exercised: an unreadable repository must surface the
// ledger error, never a fabricated view.
type failingRepo struct {
	ledger.Repository
	failGetRun           bool
	failListEvents       bool
	failListStepAttempts bool
	failListRuns         bool
}

func (f *failingRepo) GetRun(ctx context.Context, runID string) (ledger.RunSnapshot, error) {
	if f.failGetRun {
		return ledger.RunSnapshot{}, errors.New("get run boom")
	}
	return f.Repository.GetRun(ctx, runID)
}

func (f *failingRepo) ListEvents(ctx context.Context, runID string, limit, offset int) ([]ledger.EventRecord, error) {
	if f.failListEvents {
		return nil, errors.New("list events boom")
	}
	return f.Repository.ListEvents(ctx, runID, limit, offset)
}

func (f *failingRepo) ListStepAttempts(ctx context.Context, runID string) ([]ledger.StepAttempt, error) {
	if f.failListStepAttempts {
		return nil, errors.New("list attempts boom")
	}
	return f.Repository.ListStepAttempts(ctx, runID)
}

func (f *failingRepo) ListRuns(ctx context.Context, status ...ledger.RunStatus) ([]ledger.RunSnapshot, error) {
	if f.failListRuns {
		return nil, errors.New("list runs boom")
	}
	return f.Repository.ListRuns(ctx, status...)
}

func errorRepoService(t *testing.T, repo *failingRepo) *Service {
	t.Helper()
	svc, err := NewService(ServiceOptions{
		Repo: func(context.Context) (ledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func failingRepoService(t *testing.T) (*Service, *failingRepo) {
	t.Helper()
	repo := &failingRepo{Repository: ledger.NewMemoryRepository()}
	return errorRepoService(t, repo), repo
}

func TestServiceOpenRepoErrorSurfaces(t *testing.T) {
	svc, err := NewService(ServiceOptions{
		Repo: func(context.Context) (ledger.Repository, func(), error) {
			return nil, nil, errors.New("open boom")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Status(context.Background(), "run"); err == nil || !strings.Contains(err.Error(), "open boom") {
		t.Fatalf("Status(open error) = %v", err)
	}
	if _, err := svc.Events(context.Background(), "run", 1, 0); err == nil || !strings.Contains(err.Error(), "open boom") {
		t.Fatalf("Events(open error) = %v", err)
	}
	if _, err := svc.Inspect(context.Background(), "run", "step", 1, 0, 1); err == nil || !strings.Contains(err.Error(), "open boom") {
		t.Fatalf("Inspect(open error) = %v", err)
	}
	if _, err := svc.ListRuns(context.Background(), "", 1, 0); err == nil || !strings.Contains(err.Error(), "open boom") {
		t.Fatalf("ListRuns(open error) = %v", err)
	}
}

func TestServiceReadErrorsSurfaceLedgerFailures(t *testing.T) {
	svc, repo := failingRepoService(t)
	ctx := context.Background()
	// Seed one real run so the not-found guard passes for the failure paths.
	raw, err := ledger.MarshalSnapshot(ledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("name=x"), DefinitionDigest: "d",
		Inputs: map[string]string{"task": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Repository.CreateRun(ctx, ledger.RunSnapshot{
		RunID: "wfr-run", WorkflowName: "wf", WorkflowDigest: "d", Status: ledger.RunStatusPending,
		SnapshotDigest: ledger.SnapshotDigest(raw), InputDigest: ledger.InputDigest(map[string]string{"task": "x"}),
		ActiveStepID: "one",
	}, raw); err != nil {
		t.Fatal(err)
	}

	repo.failGetRun = true
	if _, err := svc.Status(ctx, "wfr-run"); err == nil || !strings.Contains(err.Error(), "get run boom") {
		t.Fatalf("Status(get error) = %v", err)
	}
	if _, err := svc.Events(ctx, "wfr-run", 1, 0); err == nil || !strings.Contains(err.Error(), "get run boom") {
		t.Fatalf("Events(get error) = %v", err)
	}
	if _, err := svc.Inspect(ctx, "wfr-run", "step", 1, 0, 1); err == nil || !strings.Contains(err.Error(), "get run boom") {
		t.Fatalf("Inspect(get error) = %v", err)
	}
	repo.failGetRun = false

	repo.failListEvents = true
	if _, err := svc.Events(ctx, "wfr-run", 1, 0); err == nil || !strings.Contains(err.Error(), "list events boom") {
		t.Fatalf("Events(list error) = %v", err)
	}
	repo.failListEvents = false

	if _, err := svc.Events(ctx, "wfr-missing", 1, 0); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Events(missing run) = %v", err)
	}

	repo.failListStepAttempts = true
	if _, err := svc.Inspect(ctx, "wfr-run", "step", 1, 0, 1); err == nil || !strings.Contains(err.Error(), "list attempts boom") {
		t.Fatalf("Inspect(attempts error) = %v", err)
	}
	repo.failListStepAttempts = false

	if _, err := svc.Inspect(ctx, "wfr-run", "nope", 1, 0, 1); err == nil || !strings.Contains(err.Error(), "not found on run") {
		t.Fatalf("Inspect(unknown attempt) = %v", err)
	}

	if _, err := svc.ListRuns(ctx, "", 1, 0); err != nil {
		t.Fatalf("ListRuns(clean) = %v", err)
	}
	repo.failListRuns = true
	if _, err := svc.ListRuns(ctx, "", 1, 0); err == nil || !strings.Contains(err.Error(), "list runs boom") {
		t.Fatalf("ListRuns(list error) = %v", err)
	}
}

func TestErrRepoUnsetHasMessage(t *testing.T) {
	if ErrRepoUnset.Error() == "" {
		t.Fatal("ErrRepoUnset.Error() must not be empty")
	}
}
