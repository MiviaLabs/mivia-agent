package cliworkflow

// Workflow run-build admission tests: the delivery admission fetch must be
// bounded (F1) and must guard the branch delivery will actually publish to,
// honoring a reserved pr_base input over the workflow-declared base (F6).

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// admissionHangGitRunner answers the origin lookup but hangs the delivery
// fetch until the context it received is done. A run blocked on a hung origin
// must be cut off by the admission timeout instead of waiting forever.
type admissionHangGitRunner struct{}

func (admissionHangGitRunner) Run(ctx context.Context, _ delivery.GitContext, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "remote":
		return "https://example.com/origin.git", nil
	case "fetch":
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Second):
			return "", errors.New("test: admission fetch was never cancelled")
		}
	case "rev-parse":
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	case "merge-base":
		return "", nil
	}
	return "", nil
}

// admissionCaptureGitRunner records the delivery fetch refspecs it answers and
// succeeds for every command, so admission completes and the pinned base is
// observable.
type admissionCaptureGitRunner struct {
	mu      sync.Mutex
	fetches []string
}

func (g *admissionCaptureGitRunner) Run(_ context.Context, _ delivery.GitContext, args ...string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "remote":
		return "https://example.com/origin.git", nil
	case "fetch":
		if len(args) >= 3 {
			g.mu.Lock()
			g.fetches = append(g.fetches, args[len(args)-1])
			g.mu.Unlock()
		}
		return "", nil
	case "rev-parse":
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	}
	return "", nil
}

func (g *admissionCaptureGitRunner) fetchRefspecs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.fetches...)
}

// TestWorkflowDeliveryAdmissionBoundedContext proves a hung admission fetch
// cannot block run creation forever: WorkflowDeliveryTimeout must bound the
// admission network calls, and the failure must surface as a context deadline
// so the run is refused with a bounded, explainable error.
func TestWorkflowDeliveryAdmissionBoundedContext(t *testing.T) {
	wf := &definition.CompiledWorkflow{
		Name: "wf-bounded",
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "main",
		},
	}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	originalTimeout := WorkflowDeliveryTimeout
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
		WorkflowDeliveryTimeout = originalTimeout
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = admissionHangGitRunner{}
	WorkflowDeliveryTimeout = 50 * time.Millisecond

	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = workflowDeliveryAdmission(wf, workflowspace.Identity{}, true, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not return; the hung fetch was not cancelled by the admission timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission error = %v, want a context deadline exceeded", err)
	}
}

// newAdmissionBaseFixture builds a delivery-active two-step fixture whose
// declared delivery base is the given branch, with a real git origin.
func newAdmissionBaseFixture(t *testing.T, base string) (root string, res *config.Resolved, wf *definition.CompiledWorkflow) {
	t.Helper()
	root = t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	setWorkflowAgentTools(t, root, "write_file")
	appendDeliveryPolicyBase(t, root, base)
	initWorkflowGitRepoWithOrigin(t, root)
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	wf, _ = compileResumeWorkflowFixture(t, root)
	return root, res, wf
}

// appendDeliveryPolicyBase adds a pull_request delivery policy with the given
// base to the two-step fixture workflow.
func appendDeliveryPolicyBase(t *testing.T, root, base string) {
	t.Helper()
	path := filepath.Join(root, ".mivia", "workflows", "two-step.toml")
	body := "\n[delivery]\nkind = \"pull_request\"\nmode = \"draft\"\nprovider = \"github\"\nbase = \"" + base + "\"\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestEffectiveDeliveryBaseResolvesPRBaseInput pins the effective-base rule:
// a valid pr_base input overrides the declared delivery base; no input, an
// empty value, or an invalid value falls back to the declared base, never an
// error.
func TestEffectiveDeliveryBaseResolvesPRBaseInput(t *testing.T) {
	wf := &definition.CompiledWorkflow{
		Name: "wf-base",
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "dev",
		},
	}
	cases := []struct {
		name   string
		inputs map[string]string
		want   string
	}{
		{"valid pr_base overrides the declared base", map[string]string{delivery.InputPRBase: "main"}, "main"},
		{"no pr_base uses the declared base", map[string]string{"task": "x"}, "dev"},
		{"empty pr_base falls back", map[string]string{delivery.InputPRBase: ""}, "dev"},
		{"invalid pr_base falls back", map[string]string{delivery.InputPRBase: "inva lid!!"}, "dev"},
		{"nil input snapshot falls back", nil, "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := delivery.EffectiveBase(wf, tc.inputs); got != tc.want {
				t.Fatalf("delivery.EffectiveBase() = %q, want %q", got, tc.want)
			}
		})
	}
}

// newAdmissionBuildFixture builds a delivery-active two-step fixture whose
// declared delivery base is the given branch, with a real git origin and an
// open store, ready for buildWorkflowController. It reuses the admission
// fixture and adds the store/repo the controller build path requires.
func newAdmissionBuildFixture(t *testing.T, base string) (root string, res *config.Resolved, store *storage.SQLite, repo workflowledger.Repository, wf *definition.CompiledWorkflow) {
	t.Helper()
	root, res, wf = newAdmissionBaseFixture(t, base)
	store, repo, closeFn, err := OpenWorkflowStore(root, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	return root, res, store, repo, wf
}

// TestAdmissionPinsEffectivePRBase proves the CLI wiring guards the branch
// delivery will actually publish to: a valid pr_base input in the run's input
// snapshot must be the branch pinned by the admission fetch, not the
// workflow-declared base. It drives the REAL build path (buildWorkflowController
// -> effective delivery base -> admission fetch) so a regression in the single
// production wiring line fails here.
func TestAdmissionPinsEffectivePRBase(t *testing.T) {
	root, res, store, repo, wf := newAdmissionBuildFixture(t, "dev")
	captured := &admissionCaptureGitRunner{}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x", delivery.InputPRBase: "main"}, []byte("definition"), "wfr-f6-prbase", nil, nil, nil, nil, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})
	refs := captured.fetchRefspecs()
	if len(refs) == 0 {
		t.Fatal("admission performed no fetch; the delivery base was never pinned")
	}
	for _, ref := range refs {
		if !strings.HasSuffix(ref, ":refs/remotes/origin/main") {
			t.Fatalf("fetch refspec = %q, want the pr_base branch (origin/main), not the declared \"dev\"", ref)
		}
	}
}

// TestAdmissionPinsDeclaredBaseWithoutPRBase proves that without a pr_base
// input the admission fetch guards the workflow-declared delivery base, driven
// through the real CLI wiring.
func TestAdmissionPinsDeclaredBaseWithoutPRBase(t *testing.T) {
	root, res, store, repo, wf := newAdmissionBuildFixture(t, "dev")
	captured := &admissionCaptureGitRunner{}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x"}, []byte("definition"), "wfr-f6-declared", nil, nil, nil, nil, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})
	refs := captured.fetchRefspecs()
	if len(refs) == 0 {
		t.Fatal("admission performed no fetch; the delivery base was never pinned")
	}
	if !strings.HasSuffix(refs[len(refs)-1], ":refs/remotes/origin/dev") {
		t.Fatalf("fetch refspec = %q, want the declared branch (origin/dev)", refs[len(refs)-1])
	}
}

// TestAdmissionFallsBackOnInvalidPRBase proves an invalid pr_base never fails
// admission: the run admits against the declared base, and the invalid input
// is left for delivery's own repairable rejection. Driven through the real CLI
// wiring with a non-empty but invalid pr_base input.
func TestAdmissionFallsBackOnInvalidPRBase(t *testing.T) {
	root, res, store, repo, wf := newAdmissionBuildFixture(t, "dev")
	captured := &admissionCaptureGitRunner{}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x", delivery.InputPRBase: "inva lid!!"}, []byte("definition"), "wfr-f6-fallback", nil, nil, nil, nil, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})
	refs := captured.fetchRefspecs()
	if len(refs) == 0 {
		t.Fatal("admission performed no fetch; the delivery base was never pinned")
	}
	if !strings.HasSuffix(refs[len(refs)-1], ":refs/remotes/origin/dev") {
		t.Fatalf("fetch refspec = %q, want the declared branch (origin/dev)", refs[len(refs)-1])
	}
}

// TestWorkflowBuildRemoteURLSkipsFetchWhenRecorded pins the resume guard: a
// recorded run never re-runs admission, so its build performs no fetch and
// records no fresh origin pin.
func TestWorkflowBuildRemoteURLSkipsFetchWhenRecorded(t *testing.T) {
	captured := &admissionCaptureGitRunner{}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = captured

	wf := &definition.CompiledWorkflow{
		Name: "wf-recorded",
		Delivery: &definition.Delivery{
			Kind: "pull_request", Mode: "draft", Provider: "github", Base: "dev",
		},
	}
	recorded := &workflowledger.RunSnapshot{RunID: "wfr-recorded"}
	url, commit, err := workflowBuildRemoteURL(wf, workflowspace.Identity{}, true, recorded, "main")
	if err != nil {
		t.Fatalf("workflowBuildRemoteURL() error = %v, want nil (a recorded run must not re-admit)", err)
	}
	if url != "" || commit != "" {
		t.Fatalf("workflowBuildRemoteURL() = (%q, %q), want empty (no fresh admission for a recorded run)", url, commit)
	}
	if len(captured.fetchRefspecs()) != 0 {
		t.Fatal("a recorded run performed a fetch; the resume guard must skip admission entirely")
	}
}

// newChildRunFixture builds a delivery-active fixture, stubs the admission
// probe and fetch (no network), and builds the controller through the real
// host wiring with the given owner session. The caller owns the returned
// build's cleanup.
func newChildRunFixture(t *testing.T, runID, ownerSessionID string, sessionRepo ledger.LedgerRepository) WorkflowControllerBuild {
	t.Helper()
	root, res, store, repo, wf := newAdmissionBuildFixture(t, "dev")
	captured := &admissionCaptureGitRunner{}
	originalProbe := workflowDeliveryProbe
	originalGit := WorkflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		WorkflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	WorkflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x"}, []byte("definition"), runID, nil, nil, nil, nil, nil, ownerSessionID, sessionRepo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		built.Dispatcher.Close()
		built.Cleanup()
	})
	return built
}

// ensureWorkflowChildRun ensures one real child run on the built controller's
// coordinator and drives the register hook the way CoordinatorRunner.dispatch
// does after its run-ID check, returning the child's run ID.
func ensureWorkflowChildRun(t *testing.T, built WorkflowControllerBuild, childRunID string) string {
	t.Helper()
	runner, ok := built.Controller.Runner.(*controller.CoordinatorRunner)
	if !ok || runner == nil {
		t.Fatalf("Controller.Runner = %T, want *controller.CoordinatorRunner", built.Controller.Runner)
	}
	h, err := runner.Coordinator.EnsureRun(context.Background(), coordinator.EnsureRunRequest{
		RunID: childRunID, Tasks: []subagents.Task{{ID: "child-1"}},
		IdempotencyKey: "workflow-step/child-register/1",
	})
	if err != nil {
		t.Fatalf("EnsureRun() error = %v", err)
	}
	if runner.RegisterChildRun == nil {
		t.Fatal("RegisterChildRun hook is not wired; the host would never register child runs")
	}
	runner.RegisterChildRun(context.Background(), h.RunID(), h)
	return h.RunID()
}

// TestBuildWorkflowControllerRegistersChildRunsUnderOwnerSession pins the
// production wiring end to end: a session-started workflow run's controller
// carries a register hook that stores each child run under the owning
// session's principal AND the session's own repo instance, and the standard
// accessible-handle gate (the one path inspect_agents/join_run/cancel_run
// share) then resolves the child through a query carrying that SESSION-side
// repo - a different instance than the workflow run's own store repo, and the
// only self-consistent shape (a query reusing the registered record's own
// lineage would prove nothing). A foreign session gets THE ONE unknown
// envelope (INV-AG-9).
func TestBuildWorkflowControllerRegistersChildRunsUnderOwnerSession(t *testing.T) {
	const owner = "sess-child-owner"
	// The session tools' repo instance. Deliberately a different instance (and
	// type) than the workflow run's own store repo: the run opens its own
	// sqlite-backed repository in newWorkflowDispatcher, and only the session
	// instance makes the child resolvable to the session that owns it.
	sessionRepo := ledger.NewMemoryLedgerRepository()
	built := newChildRunFixture(t, "wfr-child-register", owner, sessionRepo)
	childRunID := ensureWorkflowChildRun(t, built, coordinator.NewRunID())
	t.Cleanup(func() { cliorchestrate.RunHandlesForTest.Delete(childRunID) })

	// Repo-of-record is the SESSION's repo, not the workflow run's own.
	raw, ok := cliorchestrate.RunHandlesForTest.Load(childRunID)
	if !ok {
		t.Fatal("registered child record is missing")
	}
	if stamped := cliorchestrate.RepoOfHandle(raw.(*cliorchestrate.OrchestrationHandleForTest)); stamped != sessionRepo {
		t.Fatalf("record repo = %T@%v, want the owning session's repo instance", stamped, stamped)
	}

	// The owning session's tools resolve the child through a gate call that
	// carries the session repo - while coord/handle/dispatcher on the record
	// all belong to the workflow run's own wiring.
	dispatcher := built.Dispatcher.(*runtime.Dispatcher)
	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: owner})
	record, errJSON := cliorchestrate.AccessibleOrchestrationHandle(ownerCtx, childRunID, dispatcher, sessionRepo)
	if errJSON != "" {
		t.Fatalf("accessible handle errJSON = %q, want resolution for the owning session", errJSON)
	}
	if record == nil || record.GetHandle().RunID() != childRunID {
		t.Fatalf("record = %v, want the registered child handle for %q", record, childRunID)
	}
	if record.GetCoordinator() != runnerCoordinator(t, built) {
		t.Fatal("record coordinator is not the workflow run's own coordinator")
	}

	// Foreign session: unknown and inaccessible are indistinguishable.
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-other"})
	if _, unknown := cliorchestrate.AccessibleOrchestrationHandle(foreignCtx, childRunID, dispatcher, sessionRepo); unknown != `{"error":"unknown run_id"}` {
		t.Fatalf("foreign session errJSON = %q, want the one unknown envelope", unknown)
	}
	// A third repo instance (neither the session's nor matching anything on
	// the record) is locked out too: instance equality still gates access.
	if _, unknown := cliorchestrate.AccessibleOrchestrationHandle(ownerCtx, childRunID, dispatcher, ledger.NewMemoryLedgerRepository()); unknown != `{"error":"unknown run_id"}` {
		t.Fatalf("foreign repo errJSON = %q, want the one unknown envelope", unknown)
	}
}

// runnerCoordinator returns the coordinator the built controller dispatches
// through, for the ownership-vs-execution identity assertion above.
func runnerCoordinator(t *testing.T, built WorkflowControllerBuild) coordinator.Coordinator {
	t.Helper()
	runner, ok := built.Controller.Runner.(*controller.CoordinatorRunner)
	if !ok || runner == nil {
		t.Fatalf("Controller.Runner = %T, want *controller.CoordinatorRunner", built.Controller.Runner)
	}
	return runner.Coordinator
}

// TestBuildWorkflowControllerSkipsChildRunRegistrationWithoutOwner pins the
// fail-closed rule: with no owning session (the operator CLI paths) the
// register hook stays unset, so registration is skipped by design and no
// record exists for the run's children.
func TestBuildWorkflowControllerSkipsChildRunRegistrationWithoutOwner(t *testing.T) {
	built := newChildRunFixture(t, "wfr-child-no-owner", "", nil)
	runner, ok := built.Controller.Runner.(*controller.CoordinatorRunner)
	if !ok || runner == nil {
		t.Fatalf("Controller.Runner = %T, want *controller.CoordinatorRunner", built.Controller.Runner)
	}
	if runner.RegisterChildRun != nil {
		t.Fatal("RegisterChildRun hook is wired without an owning session; registration must be skipped by design")
	}

	// Sanity: the child-run ensure path itself still works, and nothing was
	// registered for it.
	h, err := runner.Coordinator.EnsureRun(context.Background(), coordinator.EnsureRunRequest{
		RunID: coordinator.NewRunID(), Tasks: []subagents.Task{{ID: "child-1"}},
		IdempotencyKey: "workflow-step/child-no-owner/1",
	})
	if err != nil {
		t.Fatalf("EnsureRun() error = %v", err)
	}
	t.Cleanup(func() { cliorchestrate.RunHandlesForTest.Delete(h.RunID()) })
	if _, ok := cliorchestrate.RunHandlesForTest.Load(h.RunID()); ok {
		t.Fatal("a no-owner run registered a child record; the fail-closed rule is broken")
	}
}

// TestBuildWorkflowControllerSkipsChildRunRegistrationWithoutSessionRepo pins
// the second fail-closed half: an owning session WITHOUT a repo (a session
// wiring that never handed one over) also skips registration - the same
// one-line skip. A record stamped with no session repo could never match any
// surface's tools, so registering it would only pretend.
func TestBuildWorkflowControllerSkipsChildRunRegistrationWithoutSessionRepo(t *testing.T) {
	built := newChildRunFixture(t, "wfr-child-no-session-repo", "sess-owner-no-repo", nil)
	runner, ok := built.Controller.Runner.(*controller.CoordinatorRunner)
	if !ok || runner == nil {
		t.Fatalf("Controller.Runner = %T, want *controller.CoordinatorRunner", built.Controller.Runner)
	}
	if runner.RegisterChildRun != nil {
		t.Fatal("RegisterChildRun hook is wired without a session repo; registration must be skipped by design")
	}
}

// TestWorkflowOwnerSessionIDEmptyWithoutCaller pins the fail-closed source:
// an admission context with no session caller yields no owner, which keeps
// the child-run registration hook unset downstream.
func TestWorkflowOwnerSessionIDResolvesAndFailsClosed(t *testing.T) {
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-owner"})
	if got := workflowOwnerSessionID(ctx); got != "sess-owner" {
		t.Fatalf("workflowOwnerSessionID = %q, want the caller's session ID", got)
	}
	if got := workflowOwnerSessionID(context.Background()); got != "" {
		t.Fatalf("workflowOwnerSessionID = %q, want empty without a session caller", got)
	}
}

// TestWorkflowChildRunRegistrarLogsRegistrationFailure pins the failure
// branch of the registration hook: a refused registration (nil handle at the
// seam boundary) logs one failure line and does not panic the dispatch path.
func TestWorkflowChildRunRegistrarLogsRegistrationFailure(t *testing.T) {
	ws, err := workspace.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	d, err := runtime.NewToolDispatcher(tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws}), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	hook := workflowChildRunRegistrar(d, nil, config.SubagentConfig{}, "session-1", ledger.NewMemoryLedgerRepository())
	if hook == nil {
		t.Fatal("registrar hook must be set when owner and session repo exist")
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	hook(context.Background(), "run-reg-fail", nil)

	if !strings.Contains(buf.String(), "child run run-reg-fail registration failed") {
		t.Fatalf("log = %q, want the registration failure line", buf.String())
	}
}
