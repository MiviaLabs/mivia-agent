package cli

// Workflow run-build admission tests: the delivery admission fetch must be
// bounded (F1) and must guard the branch delivery will actually publish to,
// honoring a reserved pr_base input over the workflow-declared base (F6).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
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
// cannot block run creation forever: workflowDeliveryTimeout must bound the
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
	originalGit := workflowDeliverGit
	originalTimeout := workflowDeliveryTimeout
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		workflowDeliverGit = originalGit
		workflowDeliveryTimeout = originalTimeout
	})
	workflowDeliveryProbe = func(string) error { return nil }
	workflowDeliverGit = admissionHangGitRunner{}
	workflowDeliveryTimeout = 50 * time.Millisecond

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
	store, repo, closeFn, err := openWorkflowStore(root, res.Subagents)
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
	originalGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		workflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	workflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x", delivery.InputPRBase: "main"}, []byte("definition"), "wfr-f6-prbase", nil, nil, nil, nil, nil)
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
	originalGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		workflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	workflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x"}, []byte("definition"), "wfr-f6-declared", nil, nil, nil, nil, nil)
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
	originalGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		workflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	workflowDeliverGit = captured

	built, err := buildWorkflowController(root, res, store, repo, wf, filepath.Join(root, ".mivia", "workflows"), map[string]any{"task": "x"}, map[string]string{"task": "x", delivery.InputPRBase: "inva lid!!"}, []byte("definition"), "wfr-f6-fallback", nil, nil, nil, nil, nil)
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
	originalGit := workflowDeliverGit
	t.Cleanup(func() {
		workflowDeliveryProbe = originalProbe
		workflowDeliverGit = originalGit
	})
	workflowDeliveryProbe = func(string) error { return nil }
	workflowDeliverGit = captured

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
