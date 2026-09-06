package clichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// chatlessCoordinator satisfies the orchestration handle registry but not
// clichat's chatCoordinator subset: GetCoordinator's narrowing misses, so
// run_messages must fall back to InitCoordinator for the dispatcher.
type chatlessCoordinator struct {
	cliorchestrate.OrchestrationCoordinator
}

// TestRunMessagesFallsBackToInitCoordinator covers the run_messages
// coordinator fallback: a stored handle whose coordinator does not satisfy
// the chat subset (assertion misses) falls back to InitCoordinator, which
// returns the already-registered real coordinator for the dispatcher.
func TestRunMessagesFallsBackToInitCoordinator(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	cfg := config.DefaultSubagentConfig
	d := runtime.New(runtime.Policy{})
	cliorchestrate.InitCoordinator(d, cfg, repo)

	// A real coordinator owns the run; the handle record stores the
	// chatless double so the first narrowing misses.
	runID := "wfr-fallback"
	if err := repo.CreateRun(context.Background(), runID, ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	cliorchestrate.StoreTestRunHandle(runID, chatlessCoordinator{}, nil, repo, d, "sess-coord-fallback")
	t.Cleanup(func() { cliorchestrate.RunHandlesForTest.Delete(runID) })

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-coord-fallback"})
	tool := &runMessagesTool{dispatcher: d, cfg: cfg, repo: repo}
	out, err := tool.Execute(ctx, json.RawMessage(`{"run_id":"wfr-fallback"}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("run_messages output not decodable: %v\n%s", err, out)
	}
	if len(resp.Messages) != 0 {
		t.Fatalf("expected zero messages for a fresh run, got %d", len(resp.Messages))
	}
}

// resumeOnlyCoordinator implements exactly cliorchestrate.ResumeCoordinator:
// it cannot be narrowed to OrchestrationCoordinator, which is the shape the
// /resume defensive fallback must tolerate.
type resumeOnlyCoordinator struct{}

func (resumeOnlyCoordinator) ListInterruptedRuns(ctx context.Context) ([]coordinator.RecoveredRun, error) {
	return nil, nil
}

func (resumeOnlyCoordinator) ResumeInterruptedRun(ctx context.Context, runID string) (*coordinator.RunHandle, error) {
	return nil, errors.New("no such run")
}

// TestHandleSlashResumeRefusesResumeOnlyCoordinator covers the fail-closed
// notice when the stored coordinator cannot serve the full resume surface.
func TestHandleSlashResumeRefusesResumeOnlyCoordinator(t *testing.T) {
	cliorchestrate.ClearAllCoordinators()
	t.Cleanup(cliorchestrate.ClearAllCoordinators)
	d := &runtime.Dispatcher{}
	cleanup := cliorchestrate.StoreTestCoordinator(d, resumeOnlyCoordinator{}, ledger.NewMemoryLedgerRepository())
	t.Cleanup(cleanup)

	term := NewTestTerminal(&bytes.Buffer{})
	if ok, _, err := handleSlashResume("/resume", []string{"/resume", "run-1"}, term); !ok || err != nil {
		t.Fatalf("handleSlashResume = (%v, %v)", ok, err)
	}
	if !strings.Contains(termOut(term), "no active orchestration runs") {
		t.Fatalf("/resume output missing fail-closed notice: %q", termOut(term))
	}
}

// TestHandleSlashResumeReachesModuleResume covers the happy-narrowing path:
// with a full coordinator registered, the /resume slash command narrows it to
// OrchestrationCoordinator and reaches ResumeRun, which fails on the unknown
// run id and renders the formatted resume error.
func TestHandleSlashResumeReachesModuleResume(t *testing.T) {
	cliorchestrate.ClearAllCoordinators()
	t.Cleanup(cliorchestrate.ClearAllCoordinators)
	d := &runtime.Dispatcher{}
	repo := ledger.NewMemoryLedgerRepository()
	cleanup := cliorchestrate.StoreTestCoordinator(d, coordinator.New(repo, nil), repo)
	t.Cleanup(cleanup)

	term := NewTestTerminal(&bytes.Buffer{})
	if ok, _, err := handleSlashResume("/resume", []string{"/resume", "run-1"}, term); !ok || err != nil {
		t.Fatalf("handleSlashResume = (%v, %v)", ok, err)
	}
	if !strings.Contains(termOut(term), "cannot resume run run-1") {
		t.Fatalf("/resume output missing formatted resume error: %q", termOut(term))
	}
}
