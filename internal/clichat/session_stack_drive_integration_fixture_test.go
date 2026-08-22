package clichat

// REAL end-to-end stack-drive integration tests through the session engine
// and its recovery sweep (the parked-stack wedge regression: a seeding
// multi-chunk plan run whose drive aborted used to sit delivery_pending
// forever - zero chunk runs, zero PRs - because the sweep only checked
// cliworkflow.StackDriveCompleted and then left it parked for 'mivia stack drive' that
// nobody ran; now reconcileParkedDelivery DRIVES the parked stack itself).
//
// Everything is real except the two host boundaries that cannot exist in a
// sandbox: the agent LLM (the scripted runner settles steps) and the gh host
// (the PR client records creates/finds and the merge seam deletes the pushed
// branch in the real bare origin, simulating GitHub's delete-branch-on-merge).
// The controllers are REAL LinearControllers, the git is REAL (worktrees,
// fetch, push, merge-base, ls-remote against a real bare origin), and
// delivery is REAL (verify eligibility, base ancestry, push, PR records).
//
// The fixture wires the seams exactly the way the shipped code does, so these
// tests prove the full pipeline - plan -> decompose -> chunk runs -> per-chunk
// PRs -> auto-merge -> integration run -> plan-run settle - end to end.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// singlePlanOutput is the decompose output of a single-mode run (the
// integration run and a single-chunk plan): nothing to stack.
const singlePlanOutput = `{"stack_mode":"single","chunk_plan":{"chunks":[]}}`

// wave1PlanOutput is the decompose output of the continuation wave (wave 1)
// for the incremental-decompose test: two more chunks, no further scope.
const wave1PlanOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c3","title":"chunk three","files":["c.go"],"est_diff_lines":10,"tests":true,"depends_on":[]},
	{"id":"c4","title":"chunk four","files":["d.go"],"est_diff_lines":10,"tests":true,"depends_on":[]}
]}}`

// hasMorePlanOutput is wave 0 with has_more=true: decompose declares more
// scope than it planned, so the drive must request a continuation wave.
const hasMorePlanOutput = `{"stack_mode":"multi","chunk_plan":{"chunks":[
	{"id":"c1","title":"chunk one","files":["a.go"],"est_diff_lines":20,"tests":true,"depends_on":[]},
	{"id":"c2","title":"chunk two","files":["b.go"],"est_diff_lines":30,"tests":true,"depends_on":["c1"]}
],"has_more":true,"remaining_scope":"the rest of the task"}}`

// stackITPRClient records PR creates and resolves FindByHead, standing in for
// the gh host's PR table. Create assigns a stable numeric RemoteID and maps it
// to the head branch so the merge seam can resolve which branch to delete.
type stackITPRClient struct {
	mu      sync.Mutex
	next    int
	byHead  map[string]delivery.PRRef // repo+"/"+head -> ref
	byNum   map[string]string         // RemoteID -> head branch
	merged  map[string]bool           // repo+"/"+head -> merged
	created []string                  // head branches in creation order
	creates int
	finds   int
}

func newStackITPRClient() *stackITPRClient {
	return &stackITPRClient{
		byHead: map[string]delivery.PRRef{},
		byNum:  map[string]string{},
		merged: map[string]bool{},
	}
}

func (c *stackITPRClient) FindByHead(_ context.Context, repo, head string) (*delivery.PRRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finds++
	ref, ok := c.byHead[repo+"/"+head]
	if !ok {
		return nil, nil
	}
	r := ref
	return &r, nil
}

func (c *stackITPRClient) Create(_ context.Context, repo string, in delivery.PRInput) (delivery.PRRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creates++
	c.next++
	num := strconv.Itoa(c.next)
	ref := delivery.PRRef{RemoteID: num, URL: "https://github.com/o/r/pull/" + num, Draft: in.Draft}
	c.byHead[repo+"/"+in.Head] = ref
	c.byNum[num] = in.Head
	c.created = append(c.created, in.Head)
	return ref, nil
}

func (c *stackITPRClient) IsMerged(_ context.Context, repo, head string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.merged[repo+"/"+head], nil
}

// markMerged records that the PR for head is merged.
func (c *stackITPRClient) markMerged(repo, head string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.merged[repo+"/"+head] = true
}

func (c *stackITPRClient) callCounts() (creates, finds int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creates, c.finds
}

// headFor resolves a PR number back to its head branch.
func (c *stackITPRClient) headFor(number string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.byNum[number]
	return h, ok
}

// stackITMergeSim is the workflowStackMergePR seam: with enabled it simulates
// GitHub's squash-merge-with-branch-deletion by deleting the PR's head branch
// in the real bare origin and recording the PR as merged. While disabled it
// refuses, which the drive treats as "not mergeable yet" (keep polling) - the
// durable park the wedge test needs.
type stackITMergeSim struct {
	prs      *stackITPRClient
	origin   string
	repo     string
	enabled  atomic.Bool
	refusals atomic.Int64
}

func (m *stackITMergeSim) Merge(_ context.Context, _ string, number string, _ bool) error {
	if !m.enabled.Load() {
		m.refusals.Add(1)
		return errors.New("simulated merge queue: checks not green")
	}
	head, ok := m.prs.headFor(number)
	if !ok {
		return fmt.Errorf("merge seam: no recorded head branch for PR %s", number)
	}
	// Record the PR as merged and delete the branch in the origin
	// (squash-merge + delete-branch). The merge oracle now checks the
	// remote PR state instead of treating branch disappearance as merge.
	m.prs.markMerged(m.repo, head)
	cmd := exec.Command("git", "--git-dir", m.origin, "update-ref", "-d", "refs/heads/"+head)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("merge seam: delete %s in origin: %v: %s", head, err, out)
	}
	return nil
}

// originGitWrapper is the cliworkflow.WorkflowDeliverGit seam. `git remote get-url
// origin` reports the github-style URL the runs record (delivery's base
// checks and ParseOwnerRepo need a github.com host), while every actual git
// operation (fetch, push, merge-base, ls-remote) is REAL and is transparently
// redirected to the local bare origin that mirrors that address.
type originGitWrapper struct {
	real        delivery.GitRunner
	localOrigin string
	remoteURL   string
}

func (g originGitWrapper) Run(ctx context.Context, gctx delivery.GitContext, args ...string) (string, error) {
	if len(args) >= 3 && args[0] == "remote" && args[1] == "get-url" && args[2] == "origin" {
		return g.remoteURL, nil
	}
	rewritten := make([]string, len(args))
	for i, a := range args {
		if a == g.remoteURL {
			rewritten[i] = g.localOrigin
		} else {
			rewritten[i] = a
		}
	}
	return g.real.Run(ctx, gctx, rewritten...)
}

// stackITRunner is the scripted agent boundary. It settles every step exactly
// as the coordinator would and, on implement, writes a REAL file into the
// run's worktree so delivery has a diff to commit and push. Failure injection
// (failFirstImplement) makes the first implement call error so the run fails
// and the drive's retry path is exercised.
type stackITRunner struct {
	root               string
	repo               workflowledger.Repository
	planOutput         string // decompose output for the plain plan run
	wave1Output        string // decompose output for continuation waves
	failFirstImplement int    // fail the first N implement calls

	mu             sync.Mutex
	implementCalls int
}

func (r *stackITRunner) RunStep(ctx context.Context, req controller.AgentStepRequest) (controller.AgentStepResult, error) {
	var output json.RawMessage
	switch req.StepID {
	case "plan":
		output = json.RawMessage(`{"summary":"ok"}`)
	case "decompose":
		dr, _ := r.repo.GetRun(ctx, req.WorkflowRunID)
		// The synthesized stacking runtime binds ONLY inputs.remaining_scope
		// into the decompose step's context; stack_mode is a reserved
		// ADMISSION input, never a step input (synthesis.go maps
		// inputs.remaining_scope, not inputs.stack_mode). Production
		// decompose detects a decompose_continue run by remaining_scope, so
		// the scripted runner must branch on it too: non-empty means
		// "decompose the continuation wave" (live finding: the runner keyed
		// on stack_mode, never saw it, and answered the continuation run
		// with wave-0's output again - duplicating c1/c2 and tripping the
		// drive's cycle guard).
		remaining, _ := req.Inputs["remaining_scope"].(string)
		log.Printf("IT-RUNNER decompose: run=%s key=%q status=%s remaining_scope=%q inputs=%v", req.WorkflowRunID, dr.InvocationKey, dr.Status, remaining, req.Inputs)
		switch {
		case strings.TrimSpace(remaining) != "":
			// decompose_continue run (wave N): decompose the remaining scope.
			output = json.RawMessage(r.wave1Output)
		case isStackIntegrationRun(dr):
			// The final full-suite run admits as stack_mode=single and must
			// emit a single-mode verdict so the router runs the workflow's
			// own implement inline (decompose(single) -> implement). A multi
			// verdict routes decompose -> chunk_plan_validate -> success and
			// SKIPS implement: the run settles with an empty worktree and
			// delivery records no_diff, so the stack "completes" with the
			// integration PR silently missing (live finding, 2026-08-16:
			// TestSessionSweepDrivesParkedStackAfterAbortedDrive failed at
			// creates=2 with the integration run's delivery record no_diff).
			// The integration run shares the plan run's decompose prompt, so
			// the scripted agent must key on the run's stable admission key
			// (<stack-id>:integration, stackAdmissionKey) - the same gap a
			// production decompose agent faces, which has no run-mode signal
			// in its step context at all.
			output = json.RawMessage(singlePlanOutput)
		default:
			// wave-0 plan run: the seeded decompose output.
			output = json.RawMessage(r.planOutput)
		}
	case "chunk_plan_validate":
		output = json.RawMessage(`{"valid":true,"reasons":[]}`)
	case "implement":
		r.mu.Lock()
		r.implementCalls++
		fail := r.implementCalls <= r.failFirstImplement
		r.mu.Unlock()
		if fail {
			return controller.AgentStepResult{}, errors.New("injected implement failure")
		}
		if err := r.writeChunkFile(ctx, req); err != nil {
			return controller.AgentStepResult{}, err
		}
		output = json.RawMessage(`{"summary":"implemented"}`)
	default:
		output = json.RawMessage(`{}`)
	}
	return controller.AgentStepResult{TaskID: req.TaskID, Output: output, EvidenceJSON: []byte(`[]`), Status: "completed"}, nil
}

// writeChunkFile writes the run's change into its real worktree so delivery
// has a diff to publish. The chunk plan declares the exact file each chunk
// may touch (delivery's diff-size gate enforces the declared slice), so the
// runner writes that declared file - a real per-chunk scope contract, not an
// arbitrary filename.
func (r *stackITRunner) writeChunkFile(ctx context.Context, req controller.AgentStepRequest) error {
	run, err := r.repo.GetRun(ctx, req.WorkflowRunID)
	if err != nil {
		return err
	}
	wt, err := vcs.Resolve(ctx, r.root, run.WorktreeName)
	if err != nil || wt == nil {
		return fmt.Errorf("resolve worktree %q: %w", run.WorktreeName, err)
	}
	chunk, _ := req.Inputs["chunk"].(string)
	file := chunkDeclaredFile(chunk)
	return os.WriteFile(filepath.Join(wt.Path, file), []byte("implemented "+chunk+"\n"), 0o600)
}

// isStackIntegrationRun reports whether the run is the final full-suite
// integration run of a stack: its stable admission key is
// <stack-id>:integration (stackAdmissionKey(stackID,
// stackIntegrationChunkID), plan D8), recorded on the run row as
// InvocationKey.
func isStackIntegrationRun(run workflowledger.RunSnapshot) bool {
	return strings.HasSuffix(run.InvocationKey, ":"+stackIntegrationChunkID)
}

// chunkDeclaredFile maps a chunk id to the file its plan slice declares
// (multiChunkPlanOutput / wave1PlanOutput). The integration run declares no
// slice, so it writes its own marker file.
func chunkDeclaredFile(chunk string) string {
	switch chunk {
	case "c1":
		return "a.go"
	case "c2":
		return "b.go"
	case "c3":
		return "c.go"
	case "c4":
		return "d.go"
	default:
		if chunk == "" {
			return "integration.txt"
		}
		return "change-" + chunk + ".txt"
	}
}

// stackDriveIT is the shared real-stack fixture. Constructing it installs the
// seam stubs (restored on cleanup); every test then drives the same real
// pipeline and asserts on the durable ledgers and the real origin.
type stackDriveIT struct {
	t          *testing.T
	root       string
	storePath  string
	configPath string
	originDir  string
	remoteURL  string
	prs        *stackITPRClient
	merges     *stackITMergeSim
	runner     *stackITRunner
	store      *storage.SQLite
	res        *config.Resolved
	repo       workflowledger.Repository
	rawDef     []byte
	compiled   *definition.CompiledWorkflow
}

func newStackDriveIT(t *testing.T, mergePolicy, planOutput string) *stackDriveIT {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	writeWorkflowRunFixture(t, root, "https://example.com", storePath)
	it := &stackDriveIT{t: t, root: root, storePath: storePath}
	it.writeMiniStackWorkflow(mergePolicy)
	initWorkflowGitRepoWithOrigin(t, root)
	rawDef, err := os.ReadFile(filepath.Join(root, ".mivia", "workflows", "mini-stack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	compiled := compileWorkflowFile(t, filepath.Join(root, ".mivia", "workflows", "mini-stack.toml"))

	it.configPath = filepath.Join(root, "config.toml")
	res, err := config.Load(config.LoadOptions{ConfigPath: it.configPath, WorkspaceRoot: root, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	cliworkflow.ApplyWorkflowStoreRoot(res, root)
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	it.originDir = filepath.Join(root, "origin.git")
	it.remoteURL = "https://github.com/o/r.git"
	it.prs = newStackITPRClient()
	it.store = store
	it.res = res
	it.repo = workflowledger.NewStorageRepository(store)
	it.rawDef = rawDef
	it.compiled = compiled
	if planOutput == "" {
		planOutput = multiChunkPlanOutput
	}
	it.runner = &stackITRunner{root: root, repo: it.repo, planOutput: planOutput, wave1Output: wave1PlanOutput}
	it.merges = &stackITMergeSim{prs: it.prs, origin: it.originDir, repo: "o/r"}
	it.merges.enabled.Store(true)

	it.installDriveSeams()
	return it
}

// writeMiniStackWorkflow writes the fixture workflow definition, optionally
// overriding the merge policy.
func (it *stackDriveIT) writeMiniStackWorkflow(mergePolicy string) {
	t := it.t
	t.Helper()
	toml := miniStackWorkflowTOML
	if mergePolicy != "" {
		toml = strings.Replace(toml, `merge_policy = "auto"`, `merge_policy = "`+mergePolicy+`"`, 1)
	}
	if err := os.WriteFile(filepath.Join(it.root, ".mivia", "workflows", "mini-stack.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// installDriveSeams installs the test's delivery/git/build seam stubs and
// restores the production values on cleanup (reverse order).
func (it *stackDriveIT) installDriveSeams() {
	t := it.t
	t.Helper()
	prevMergePR := workflowStackMergePR
	prevPR := cliworkflow.WorkflowDeliverNewPR
	prevGit := cliworkflow.WorkflowDeliverGit
	prevBuild := cliworkflow.WorkflowRunBuild
	prevPoll := stackMergePollInterval
	prevBound := workflowAutoDeliveryAttemptTimeout
	prevDrive := cliworkflow.WorkflowStackDriveToCompletion
	t.Cleanup(func() {
		cliworkflow.WorkflowStackDriveToCompletion = prevDrive
		workflowAutoDeliveryAttemptTimeout = prevBound
		stackMergePollInterval = prevPoll
		cliworkflow.WorkflowRunBuild = prevBuild
		cliworkflow.WorkflowDeliverGit = prevGit
		cliworkflow.WorkflowDeliverNewPR = prevPR
		workflowStackMergePR = prevMergePR
	})
	workflowStackMergePR = it.merges.Merge
	cliworkflow.WorkflowDeliverNewPR = func() delivery.PRClient { return it.prs }
	cliworkflow.WorkflowDeliverGit = originGitWrapper{real: delivery.RealGit{}, localOrigin: it.originDir, remoteURL: it.remoteURL}
	stackMergePollInterval = 100 * time.Millisecond
	cliworkflow.WorkflowStackDriveToCompletion = driveStackToCompletion // production default, pinned explicitly
	cliworkflow.WorkflowRunBuild = it.buildStub
}

// buildStub is the cliworkflow.WorkflowRunBuild seam: it runs the SAME scripted step
// runtimes the production build path would, in the run's per-run worktree
// (the isolation cliworkflow.SelectWorkflowWorkspace provides), so the controller sees a
// faithful build result without invoking real tool runs.
func (it *stackDriveIT) buildStub(buildRoot string, _ *config.Resolved, _ *storage.SQLite, repo workflowledger.Repository, compiled *definition.CompiledWorkflow, _ string, _ map[string]any, inputSnapshot map[string]string, _ []byte, id string, _ *workflowledger.Snapshot, _ []byte, _ *workflowledger.RunSnapshot, _ map[string]bool, _ *skills.Registry) (cliworkflow.WorkflowControllerBuild, error) {
	identity, cleanup, err := cliworkflow.SelectWorkflowWorkspace(context.Background(), buildRoot, id, true, nil)
	if err != nil {
		return cliworkflow.WorkflowControllerBuild{}, err
	}
	synth, err := definition.SynthesizeStacking(compiled)
	if err != nil {
		cleanup()
		return cliworkflow.WorkflowControllerBuild{}, err
	}
	steps := scriptedMiniStackRuntimes(it.t, synth)
	snap := miniStackSnapshot(it.t, it.root, compiled, it.rawDef)
	snap.Inputs = inputSnapshot
	rawSnapshot, err := workflowledger.MarshalSnapshot(snap)
	if err != nil {
		cleanup()
		return cliworkflow.WorkflowControllerBuild{}, err
	}
	inputs := make(map[string]any, len(inputSnapshot))
	for k, v := range inputSnapshot {
		inputs[k] = v
	}
	ctrl, err := controller.NewLinearController(repo, it.runner, synth, steps, inputs, id, rawSnapshot)
	if err != nil {
		cleanup()
		return cliworkflow.WorkflowControllerBuild{}, err
	}
	return cliworkflow.WorkflowControllerBuild{
		Controller: ctrl,
		Dispatcher: workflowTestDispatcher{},
		Admission: controller.Admission{
			BaseRef: identity.BaseRef, BaseCommit: identity.BaseCommit,
			OriginBaseCommit: identity.OriginBaseCommit, WorktreeName: identity.WorktreeName,
			RemoteURL: it.remoteURL, InputDigest: workflowledger.InputDigest(inputSnapshot),
		},
		Cleanup: cleanup,
	}, nil
}

// startPlanRun launches the mini-stack plan run through the REAL session
// engine surface: the synchronous StartNew admission, then
// LaunchStartedWorkflow (real controller + the auto-delivery repair loop that
// drives the stack) - the exact order startCLI uses, because
// LaunchStartedWorkflow reads the run right after spawning its goroutine and
// production admits synchronously first. It returns when the engine's
// goroutine has fully exited (idle), so the caller observes the durable end
// state of the drive attempt.
func (it *stackDriveIT) startPlanRun(bound time.Duration) string {
	t := it.t
	t.Helper()
	prevBound := workflowAutoDeliveryAttemptTimeout
	t.Cleanup(func() { workflowAutoDeliveryAttemptTimeout = prevBound })
	workflowAutoDeliveryAttemptTimeout = bound

	prepared, err := cliworkflow.PrepareWorkflowRun("mini-stack", it.root, it.configPath, []string{"task=x"})
	if err != nil {
		t.Fatal(err)
	}
	runID := cliworkflow.NewCLIWorkflowRunID()
	built, err := cliworkflow.WorkflowRunBuild(prepared.Root, prepared.Res, prepared.Store, prepared.Repo, prepared.Compiled, prepared.RefBase, prepared.Inputs, prepared.InputSnapshot, prepared.Raw, runID, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	finish, err := cliworkflow.BeginWorkflowExecution(prepared.Root, ContextStorePath(prepared.Root, prepared.Res.Subagents), runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cliworkflow.WorkflowRunSetAdmission(built); err != nil {
		t.Fatal(err)
	}
	created, err := built.Controller.StartNew(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("plan run %s was not admitted as a fresh run", runID)
	}
	// The engine must resolve the SAME store the fixture writes. The sweep's
	// config.Load uses cliworkflow.WorkflowConfigPath(root, engine.configPath): with ""
	// it probes .mivia/mivia.toml (absent in the fixture) and falls back to
	// defaults, so cliworkflow.ApplyWorkflowStoreRoot pins root/context.db while the
	// runs live in root/workflow.db (the fixture's config.toml store_path) -
	// the sweep then logged "0 parked run(s)" and the parked-stack backstop
	// was dead (live finding: every sweep in this suite found nothing, so
	// the retry/grant tests stalled at delivery_pending forever). Passing
	// the fixture's config path mirrors production (workflow_tool_service.go
	// constructs the engine with the session's config path).
	e := cliworkflow.NewSessionWorkflowEngine(it.root, it.configPath)
	if _, err := e.LaunchStartedWorkflow(context.Background(), prepared, built, runID, "mini-stack", finish); err != nil {
		t.Fatal(err)
	}
	// The in-session drive runs for up to the attempt bound (merges that never
	// land keep it polling the full bound), so the idle wait must exceed the
	// bound or it fails while the drive is still legitimately advancing.
	cliworkflow.WaitForSessionEngineIdleWithin(t, e, runID, bound+10*time.Second)
	return runID
}

// runSweep runs one full recovery sweep with a FRESH engine - the durable
// backstop that must drive a parked stack to completion. quiet=false: the
// test must SEE the sweep's drive errors, not swallow them.
func (it *stackDriveIT) runSweep() {
	it.t.Helper()
	// Same configPath as startPlanRun: the sweep must open the store the
	// runs actually live in (see the note there).
	e := cliworkflow.NewSessionWorkflowEngine(it.root, it.configPath)
	e.ReconcileParkedRuns(context.Background(), false)
}

// deliverRun grants publication for one delivery_pending run (the human's
// `mivia workflow deliver <run> --allow-publish`) with the REAL delivery
// pipeline.
func (it *stackDriveIT) deliverRun(runID string) {
	it.t.Helper()
	if err := cliworkflow.DeliverRunWithStore(context.Background(), it.root, it.res, it.store, it.repo, runID, true, false, io.Discard, io.Discard); err != nil {
		it.t.Fatalf("deliver %s: %v", runID, err)
	}
}

// mergeBranch simulates a human merging the run's PR on GitHub: the branch
// disappears from the origin (GitHub's default delete-branch-on-merge).
func (it *stackDriveIT) mergeBranch(runID string) {
	it.t.Helper()
	run, err := it.repo.GetRun(context.Background(), runID)
	if err != nil {
		it.t.Fatal(err)
	}
	head := stackHeadBranch(run)
	if head == "" {
		it.t.Fatalf("run %s has no head branch", runID)
	}
	// Simulate the human merge: record the PR as merged and delete the branch.
	it.prs.markMerged("o/r", head)
	cmd := exec.Command("git", "--git-dir", it.originDir, "update-ref", "-d", "refs/heads/"+head)
	if out, err := cmd.CombinedOutput(); err != nil {
		it.t.Fatalf("simulate merge of %s: %v: %s", head, err, out)
	}
}

// runStatus reads one run's ledger status.
func (it *stackDriveIT) runStatus(runID string) workflowledger.RunStatus {
	it.t.Helper()
	run, err := it.repo.GetRun(context.Background(), runID)
	if err != nil {
		it.t.Fatal(err)
	}
	return run.Status
}

// taskStatuses reads every stack task's durable status.
func (it *stackDriveIT) taskStatuses(stackID string) map[string]string {
	it.t.Helper()
	byID, err := stackTaskMap(workflowledger.NewStore(it.store), stackID)
	if err != nil {
		it.t.Fatal(err)
	}
	out := make(map[string]string, len(byID))
	for id, t := range byID {
		out[id] = t.Status
	}
	return out
}

// originHeads lists the wf/* branches left on the real origin.
func (it *stackDriveIT) originHeads() []string {
	it.t.Helper()
	out, err := exec.Command("git", "--git-dir", it.originDir, "for-each-ref", "--format=%(refname:short)", "refs/heads/wf/").CombinedOutput()
	if err != nil {
		it.t.Fatalf("list origin wf refs: %v: %s", err, out)
	}
	var heads []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			heads = append(heads, strings.TrimSpace(line))
		}
	}
	return heads
}
