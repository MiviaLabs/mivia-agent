package cli

// diff_coverage_gaps_test.go closes the remaining uncovered statement lines
// reported by the diff-coverage gate for internal/cli: router branches that
// reach their handlers with fast-failing arguments, the stack command's
// dispatch and status paths, and the seam-wiring closures.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// --- root.go router branches ----------------------------------------------

// Each command reaches its handler through the router; the arguments are
// chosen so the handler refuses immediately (missing flag value, missing
// subcommand) instead of touching a real workspace or starting a TUI.
func TestExecuteRouterReachesRemainingHandlers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := [][]string{
		{"chat", "--provider"},      // parse refusal before any workspace work
		{"doctor", "--config"},      // flag parse refusal
		{"agents", "bogus"},         // unknown agents subcommand
		{"sessions"},                // missing sessions subcommand
		{"compact"},                 // missing --session
		{"workflow", "--workspace"}, // flag parse refusal
	}
	for _, args := range calls {
		if err := Execute(args); err == nil {
			t.Errorf("Execute(%v) = nil, want a fast refusal error", args)
		}
	}
}

// --- stack_command.go ------------------------------------------------------

// wireStackSeams installs working OpenStackLedgerFunc / ResolveStackIDFunc
// implementations for the cli test binary (the production wiring lives in
// unexported clichat code the test shims cannot reach) and restores the
// previous values when the test ends.
func wireStackSeams(t *testing.T) {
	t.Helper()
	savedOpen, savedResolve := clichat.OpenStackLedgerFunc, clichat.ResolveStackIDFunc
	clichat.OpenStackLedgerFunc = func(root, configPath string) (*workflowledger.Store, workflowledger.Repository, func(), error) {
		if strings.TrimSpace(root) == "" {
			root = "."
		}
		work, err := workspace.Open(root)
		if err != nil {
			return nil, nil, nil, err
		}
		cfgPath := cliworkflow.WorkflowConfigPath(work.Abs, configPath)
		res, err := config.Load(config.LoadOptions{
			ConfigPath: cfgPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		cliworkflow.ApplyWorkflowStoreRoot(res, work.Abs)
		store, repo, closeFn, err := cliworkflow.OpenWorkflowStore(work.Abs, res.Subagents)
		if err != nil {
			return nil, nil, nil, err
		}
		return workflowledger.NewStore(store), repo, closeFn, nil
	}
	clichat.ResolveStackIDFunc = func(repo workflowledger.Repository, workflowName, stackFlag string) (string, error) {
		if strings.TrimSpace(stackFlag) != "" {
			return stackFlag, nil
		}
		runs, err := repo.ListRuns(context.Background())
		if err != nil {
			return "", err
		}
		best, bestStarted := "", time.Time{}
		for _, r := range runs {
			if r.WorkflowName != workflowName || r.InvocationKey != "" {
				continue
			}
			attempts, err := repo.ListStepAttempts(context.Background(), r.RunID)
			if err != nil {
				continue
			}
			planRun := false
			for _, a := range attempts {
				if a.StepID == delivery.DecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded {
					planRun = true
				}
			}
			if !planRun {
				continue
			}
			if best == "" || r.StartedAt.After(bestStarted) {
				best, bestStarted = r.RunID, r.StartedAt
			}
		}
		if best == "" {
			return "", fmt.Errorf("no plan-mode run found for workflow %q; run `mivia stack plan %s` first", workflowName, workflowName)
		}
		return best, nil
	}
	t.Cleanup(func() {
		clichat.OpenStackLedgerFunc = savedOpen
		clichat.ResolveStackIDFunc = savedResolve
	})
}

// overrideStackLedger replaces the OpenStackLedgerFunc seam with fixed
// in-memory stores for one test and restores it afterwards.
func overrideStackLedger(t *testing.T, ledger *workflowledger.Store, repo workflowledger.Repository) {
	t.Helper()
	saved := clichat.OpenStackLedgerFunc
	clichat.OpenStackLedgerFunc = func(string, string) (*workflowledger.Store, workflowledger.Repository, func(), error) {
		return ledger, repo, func() {}, nil
	}
	t.Cleanup(func() { clichat.OpenStackLedgerFunc = saved })
}

func TestRunStackWithIOFastFailures(t *testing.T) {
	wireStackSeams(t)
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	var out, errOut strings.Builder

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", nil, "expected plan, drive, or status"},
		{"unknown subcommand", []string{"bogus"}, "unknown subcommand"},
		{"status without name", []string{"status"}, "expected a workflow name"},
	}
	for _, tc := range cases {
		out.Reset()
		errOut.Reset()
		err := runStackWithIO(tc.args, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: runStackWithIO = %v, want error containing %q", tc.name, err, tc.want)
		}
	}

	// plan refusal against an empty workspace.
	out.Reset()
	errOut.Reset()
	if err := runStackWithIO([]string{"plan", "ghostwf", "--workspace", workspace}, &out, &errOut); err == nil {
		t.Error("stack plan ghostwf must error on an empty workspace")
	}

	// status refusals against an empty (memory-backed) ledger: an unknown
	// workflow has no plan-mode run, and an explicit --stack id has no tasks.
	overrideStackLedger(t, workflowledger.NewMemoryStore(), workflowledger.NewMemoryRepository())
	out.Reset()
	errOut.Reset()
	if err := runStackWithIO([]string{"status", "ghostwf", "--workspace", workspace}, &out, &errOut); err == nil ||
		!strings.Contains(err.Error(), "no plan-mode run") {
		t.Errorf("stack status ghostwf = %v, want the no-plan-mode-run error", err)
	}
	out.Reset()
	errOut.Reset()
	err := runStackWithIO([]string{"status", "wf", "--stack", "stack-none", "--workspace", workspace}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "no chunk tasks") {
		t.Fatalf("stack status --stack stack-none = %v, want the no-chunk-tasks error", err)
	}
	// drive against the same empty ledger refuses at plan-input resolution.
	out.Reset()
	errOut.Reset()
	if err := runStackWithIO([]string{"drive", "ghostwf", "--workspace", workspace}, &out, &errOut); err == nil {
		t.Error("stack drive ghostwf must error on an empty workspace")
	}

	// A workspace root that is a regular file makes the ledger open fail.
	fileRoot := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	err = runStackWithIO([]string{"status", "wf", "--workspace", fileRoot}, &out, &errOut)
	if err == nil {
		t.Fatal("stack status on a file workspace root must error")
	}
}

// The full status path: seeded task ledger plus run ledger so the run/PR
// join and the publish-grant hint both execute.
func TestRunStackStatusSeededStack(t *testing.T) {
	wireStackSeams(t)
	t.Setenv("HOME", t.TempDir())
	ledger := workflowledger.NewMemoryStore()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })

	const stackID = "stack-live"
	scope := clichat.StackScope(stackID)
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{
		ID: "c1", PlanRef: stackID, Scope: scope, Status: "reviewed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.CreateTask(workflowledger.Task{
		ID: "c2", PlanRef: stackID, Scope: scope, Status: "merged",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRun(context.Background(), workflowledger.RunSnapshot{
		RunID: "wfr-c1", WorkflowName: "wf", InvocationKey: stackID + ":c1",
		Status: workflowledger.RunStatusPending,
	}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	// Advance the run to delivery_pending through the ledger's own CAS
	// transitions so the snapshot state is valid.
	for _, to := range []workflowledger.RunStatus{workflowledger.RunStatusRunning, workflowledger.RunStatusDeliveryPending} {
		stored, err := repo.GetRun(context.Background(), "wfr-c1")
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), "wfr-c1", stored.Version, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: "wfr-c1", IdempotencyKey: "k1", Mode: "pr",
		URL: "https://github.com/acme/r/pull/7", Status: "published",
	}); err != nil {
		t.Fatal(err)
	}

	overrideStackLedger(t, ledger, repo)

	var out strings.Builder
	if err := runStackWithIO([]string{"status", "wf", "--stack", stackID}, &out, io.Discard); err != nil {
		t.Fatalf("runStackWithIO(status seeded) = %v, want nil", err)
	}
	got := out.String()
	for _, want := range []string{"c1", "reviewed", "wfr-c1", "\t7\t", "awaits the publish grant"} {
		if !strings.Contains(got, want) {
			t.Errorf("status output missing %q; got:\n%s", want, got)
		}
	}
}

// --- clichat_wiring.go / cliworkflow_wiring.go closures --------------------

// The production CurrentHookSessionFunc closure must be invocable and must
// wrap the (possibly nil) live hook session without panicking.
func TestCurrentHookSessionFuncWiringClosure(t *testing.T) {
	if clichat.CurrentHookSessionFunc == nil {
		t.Fatal("clichat.CurrentHookSessionFunc must be wired by init")
	}
	state := clichat.CurrentHookSessionFunc()
	if state == nil {
		t.Fatal("CurrentHookSessionFunc() must return a non-nil adapter")
	}
}

// The production ClassifyStackPlanRunDeliveryFunc closure must run against
// a repository without panicking; an unknown run classifies as not
// applicable long before any store access.
func TestClassifyStackPlanRunDeliveryWiringClosure(t *testing.T) {
	if cliworkflow.ClassifyStackPlanRunDeliveryFunc == nil {
		t.Fatal("cliworkflow.ClassifyStackPlanRunDeliveryFunc must be wired by init")
	}
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	gate := cliworkflow.ClassifyStackPlanRunDeliveryFunc(context.Background(), t.TempDir(), nil, repo, "wfr-missing", false)
	if gate < 0 {
		t.Fatalf("ClassifyStackPlanRunDeliveryFunc(unknown run) = %d, want a non-negative gate", gate)
	}
}

// --- memory_command.go -----------------------------------------------------

func TestParseMemoryDumpArgsRejectsPositional(t *testing.T) {
	_, _, err := parseMemoryDumpArgs([]string{"stray"})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("parseMemoryDumpArgs(stray) = %v, want the unexpected-argument error", err)
	}
}
