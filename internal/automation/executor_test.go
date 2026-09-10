package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// recordingConversation is executor_test.go's own fake ports.Conversation:
// it records every prompt text sent to it, in order, and can be told to
// fail starting at a given call index (1-based count) so a test can
// observe the executor's per-step checkpointing behavior around a
// mid-run failure. Every successful Send returns a turn handle whose
// Events() channel is already closed - an empty, successful turn - which
// sendTurnHeadless's drain-to-close loop handles correctly with zero
// events.
type recordingConversation struct {
	mu       sync.Mutex
	sent     []string
	failAt   int // 1-based Send call count to fail at; 0 means never fail
	failWith error
	// onSend, when non-nil, is invoked synchronously on every successful
	// Send call, AFTER recording the text but BEFORE returning - a test
	// hook letting a caller mutate shared state (e.g. drop a table)
	// precisely between one step's own completion and runSteps' own
	// immediately-following checkpoint call, which has no other
	// interleaving point to target deterministically.
	onSend func()
}

func newRecordingConversation() *recordingConversation {
	return &recordingConversation{}
}

func (c *recordingConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.sent = append(c.sent, in.Text)
	n := len(c.sent)
	onSend := c.onSend
	c.mu.Unlock()
	if c.failAt > 0 && n == c.failAt {
		if c.failWith != nil {
			return nil, c.failWith
		}
		return nil, fmt.Errorf("recordingConversation: forced failure at call %d", n)
	}
	if onSend != nil {
		onSend()
	}
	ch := make(chan uievent.Event)
	close(ch)
	return &fakeTurnHandle{ch: ch}, nil
}

func (c *recordingConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *recordingConversation) History() []ports.Message             { return nil }
func (c *recordingConversation) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (c *recordingConversation) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *recordingConversation) Title() string                        { return "" }
func (c *recordingConversation) ID() string                           { return "exec-conv" }

func (c *recordingConversation) sentTexts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.sent))
	copy(out, c.sent)
	return out
}

// approvalOverride records one SetApprovalOverride call's arguments.
type approvalOverride struct {
	sessionID string
	gate      func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult
	policy    string
}

// fakeExecSpawner is executor_test.go's own SessionSpawner double: it
// hands out ONE recordingConversation per test (mirroring D2's "several
// steps, one session"), and records every SetApprovalOverride call so a
// test can invoke the captured gate directly and inspect the installed
// policy string. createErr, when set, makes CreateFreshInDir fail
// without ever calling bind - proving the executor's own worktree
// failure path never reaches session spawning.
type fakeExecSpawner struct {
	mu             sync.Mutex
	conv           *recordingConversation
	createCalls    int
	createErr      error
	setApprovalErr error
	overrides      []approvalOverride
}

func (f *fakeExecSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	f.mu.Lock()
	f.createCalls++
	f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	if bind != nil {
		if _, err := bind(nil); err != nil {
			return nil, err
		}
	}
	return f.conv, nil
}

func (f *fakeExecSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setApprovalErr != nil {
		return f.setApprovalErr
	}
	f.overrides = append(f.overrides, approvalOverride{sessionID: sessionID, gate: gate, policy: policy})
	return nil
}

func (f *fakeExecSpawner) lastOverride() (approvalOverride, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.overrides) == 0 {
		return approvalOverride{}, false
	}
	return f.overrides[len(f.overrides)-1], true
}

func (f *fakeExecSpawner) createCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createCalls
}

// seedEnabledAutomation writes automations.toml at root with one enabled
// automation carrying the given steps and unattended policy, returning
// its ID.
func seedEnabledAutomation(t *testing.T, root string, mutate func(*Spec)) string {
	t.Helper()
	spec := Spec{
		ID:      "exec-auto",
		Name:    "exec-auto",
		Enabled: true,
		Steps:   []Step{{Kind: StepPrompt, Prompt: "hello"}},
	}
	if mutate != nil {
		mutate(&spec)
	}
	if err := SaveSpecs(ports.ScopeProject, root, []Spec{spec}); err != nil {
		t.Fatalf("SaveSpecs: %v", err)
	}
	return spec.ID
}

// TestRunOnceDedupConcurrent is the plan's dedup negative test (D7): a
// fire that arrives while another is already holding the fenced claim
// for the same automation is a documented no-op, not an error - it
// records and returns a RunSkipped run. Rather than relying on real
// goroutine scheduling to force two RunOnce calls to overlap (flaky:
// this package's own fake conversation completes a run so fast that two
// goroutines routinely run sequentially without ever contending), this
// test makes the collision deterministic by pre-acquiring the claim
// exactly as a genuinely-concurrent first call would hold it, then
// calling RunOnce and asserting the second-fire behavior directly.
// TestAdmitFireDedupConcurrent (runstore_test.go) already covers the
// claim primitive's own real-concurrency behavior; this test's job is
// only to prove RunOnce reacts to a lost claim exactly as D7 specifies.
func TestRunOnceDedupConcurrent(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// Simulate a fire already in flight: win the claim ourselves first,
	// exactly as the first of two genuinely-concurrent RunOnce calls
	// would have.
	_, ok, err := svc.admitFire(ctx, automationID)
	if err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	run, err := svc.RunOnce(ctx, automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce while claim held: got err=%v, want nil (lost claim is a no-op)", err)
	}
	if run.State != ports.RunSkipped {
		t.Fatalf("RunOnce while claim held: got state %v, want RunSkipped", run.State)
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("RunOnce while claim held: spawn.CreateFreshInDir called %d times, want 0 (a skipped fire must have zero side effects)", spawn.createCallCount())
	}

	// Release the held claim, then confirm a fresh fire now wins and
	// runs for real - proving the skip above was genuinely about the
	// collision, not a permanently broken claim path.
	claim, err := db.GetClaim(ctx, claimKey(automationID))
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if err := db.ReleaseClaimFenced(ctx, claim); err != nil {
		t.Fatalf("ReleaseClaimFenced: %v", err)
	}

	run2, err := svc.RunOnce(ctx, automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce after release: got err=%v, want nil", err)
	}
	if run2.State != ports.RunSucceeded {
		t.Fatalf("RunOnce after release: got state %v, want RunSucceeded", run2.State)
	}
}

// TestRunOnceMixedActionOneSessionInOrder covers the plan's "mixed
// action, one session, in order" test: StepPrompt, StepSkill, StepSlash
// dispatch on the SAME spawned conversation, in spec order.
//
// Deviation from the plan's literal step-kind list: StepAgent and
// StepWorkflow are deliberately excluded from this ordering test.
// StepAgent's ApplySessionAgent call needs a real *chat.Session with a
// wired AgentSessionState.Registry to select a NAMED agent (this
// package's Service carries neither - see runStep's own StepAgent
// comment, a documented gap of chunk 6 as specified); StepWorkflow
// dispatches to a REAL cliworkflow.NewSessionWorkflowEngine, which needs
// an actual .mivia/workflows/<name> definition on disk to resolve
// Start's Workflow lookup. Exercising either here would test
// cliworkflow/cliagents machinery, not this file's own dispatch-order
// logic. StepWorkflow's own zero-value AllowPublish contract is pinned
// separately below (TestNewWorkflowStartRequestNeverSetsAllowPublish).
func TestRunOnceMixedActionOneSessionInOrder(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{
			{Kind: StepPrompt, Prompt: "hello"},
			{Kind: StepSkill, Ref: "foo"},
			{Kind: StepSlash, Ref: "/compact"},
		}
	})
	conv := newRecordingConversation()
	spawn := &fakeExecSpawner{conv: conv}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce State = %v, want RunSucceeded: %+v", run.State, run)
	}
	want := []string{"hello", "/foo", "/compact"}
	got := conv.sentTexts()
	if len(got) != len(want) {
		t.Fatalf("sent texts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sent texts = %v, want %v (order mismatch at %d)", got, want, i)
		}
	}
	if spawn.createCallCount() != 1 {
		t.Fatalf("CreateFreshInDir called %d times, want exactly 1 (one session for the whole mixed action)", spawn.createCallCount())
	}
}

// TestNewWorkflowStartRequestNeverSetsAllowPublish pins D8's "AllowPublish
// is never set for StepWorkflow" requirement at the level this package
// controls: the StartRequest newWorkflowStartRequest builds always
// carries AllowPublish=false, whatever the automation's Unattended
// policy is - the executor never wires unattended-auto into workflow
// delivery authority.
func TestNewWorkflowStartRequestNeverSetsAllowPublish(t *testing.T) {
	step := Step{Kind: StepWorkflow, Ref: "release-notes", Inputs: map[string]string{"branch": "main"}}
	req := newWorkflowStartRequest("run-1", 2, step)
	if req.AllowPublish {
		t.Fatal("newWorkflowStartRequest set AllowPublish=true, want it to always stay false (D8)")
	}
	if req.Workflow != "release-notes" || req.InvocationKey != "run-1:2" {
		t.Fatalf("newWorkflowStartRequest = %+v, want Workflow=release-notes InvocationKey=run-1:2", req)
	}
	if req.Inputs["branch"] != "main" {
		t.Fatalf("newWorkflowStartRequest Inputs = %v, want branch=main", req.Inputs)
	}
}

// TestRunOnceWorktreeCreationFailureNoSessionNoOrphan covers D3's
// documented failure contract: a WorktreeNew automation whose worktree
// creation fails (here, deterministically: cliworktree's process-global
// OpenRepositoryContextStoreFunc is never wired in this test binary) ends
// the run RunFailed with ZERO side effects - no session ever spawned, and
// no worktree directory left on disk.
func TestRunOnceWorktreeCreationFailureNoSessionNoOrphan(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Worktree = WorktreeNew
		s.BaseRef = "HEAD"
	})
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: got error %v, want nil (failure is recorded on the run, not returned)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed", run.State)
	}
	if run.FailKind != ports.RunFailJobError {
		t.Fatalf("RunOnce FailKind = %v, want RunFailJobError", run.FailKind)
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (worktree failure must precede session spawn)", spawn.createCallCount())
	}
	wtDir := workspace.WorktreesDir(root)
	if entries, err := os.ReadDir(wtDir); err == nil && len(entries) != 0 {
		t.Fatalf("worktrees dir %q has %d entries, want 0 (no orphan worktree)", wtDir, len(entries))
	}
}

// TestRunOnceChecksPointsStepIndex covers the plan's "checkpoints step
// index" test: a 3-step run whose THIRD step fails must have already
// checkpointed steps 0 and 1 (StepIndex advanced to 1 then 2) before the
// failure - proving updateRunState runs after EVERY successful step, not
// only once at the end.
func TestRunOnceChecksPointsStepIndex(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{
			{Kind: StepPrompt, Prompt: "one"},
			{Kind: StepPrompt, Prompt: "two"},
			{Kind: StepPrompt, Prompt: "three"},
		}
	})
	conv := newRecordingConversation()
	conv.failAt = 3 // fail on the third Send call (step index 2)
	spawn := &fakeExecSpawner{conv: conv}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed", run.State)
	}
	// runToPorts's ports.Run carries no StepIndex field; read the durable
	// row directly via the runstore to check the checkpointed value.
	stored, ok, err := svc.getRun(context.Background(), run.ID)
	if err != nil || !ok {
		t.Fatalf("getRun after failure: ok=%v err=%v", ok, err)
	}
	if stored.StepIndex != 2 {
		t.Fatalf("StepIndex after failure at step 2 = %d, want 2 (steps 0 and 1 checkpointed first)", stored.StepIndex)
	}
	if !strings.Contains(stored.Message, "forced failure") {
		t.Fatalf("stored.Message = %q, want it to name the forced failure", stored.Message)
	}
}

// TestRunOnceUnattendedDenyBlocksApprovalRequiringCall covers D8's
// default posture: an automation with Unattended=UnattendedDeny (or
// unset) installs DenyGate on the spawned session's approval override
// with policy "deny", and invoking the captured gate on any call refuses
// it, naming the automation.
func TestRunOnceUnattendedDenyBlocksApprovalRequiringCall(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil) // Unattended left at zero value ("")
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce State = %v, want RunSucceeded", run.State)
	}
	override, ok := spawn.lastOverride()
	if !ok {
		t.Fatal("no SetApprovalOverride call was recorded")
	}
	if override.policy != "deny" {
		t.Fatalf("installed policy = %q, want deny", override.policy)
	}
	result := override.gate(context.Background(), "run_command", json.RawMessage(`{"command":"rm -rf /"}`))
	if result.Approved {
		t.Fatal("installed deny gate approved a call; want every call denied")
	}
	if !strings.Contains(result.Err, automationID) {
		t.Fatalf("installed deny gate error = %q, want it naming the automation %q", result.Err, automationID)
	}
}

// TestRunOnceUnattendedAutoInstallsOverrideNotAmbientPosture covers D8's
// opt-in posture: Unattended=UnattendedAuto installs AutoApproveGate with
// policy "auto" and ApprovedForClass=false, UNCONDITIONALLY - the
// override is installed fresh on every run regardless of whatever
// ambient/default approval posture a spawned session might otherwise
// carry, proving the executor never inherits an ambient posture rather
// than installing its own.
func TestRunOnceUnattendedAutoInstallsOverrideNotAmbientPosture(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Unattended = UnattendedAuto
	})
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if run.State != ports.RunSucceeded {
		t.Fatalf("RunOnce State = %v, want RunSucceeded", run.State)
	}
	override, ok := spawn.lastOverride()
	if !ok {
		t.Fatal("no SetApprovalOverride call was recorded")
	}
	if override.policy != "auto" {
		t.Fatalf("installed policy = %q, want auto (never inherited from any ambient default)", override.policy)
	}
	result := override.gate(context.Background(), "edit_file", json.RawMessage(`{"path":"a.txt"}`))
	if !result.Approved {
		t.Fatal("installed auto gate denied a call; want every call approved")
	}
	if result.ApprovedForClass {
		t.Fatal("installed auto gate set ApprovedForClass; unattended auto must never persist a standing decision")
	}
}

// TestRunOnceNotFound covers RunOnce's own 404 case: an unknown
// automation ID is rejected with ErrAutomationNotFound.
func TestRunOnceNotFound(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = svc.RunOnce(context.Background(), "no-such-automation", ports.TriggerManual)
	if err == nil {
		t.Fatal("RunOnce(unknown id): got nil error, want ErrAutomationNotFound")
	}
}

// TestRunOnceDisabledRefused covers RunOnce's disabled-automation guard:
// a spec that exists but is Enabled=false is refused rather than run.
func TestRunOnceDisabledRefused(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Enabled = false
	})
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err == nil {
		t.Fatal("RunOnce(disabled automation): got nil error, want rejection")
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times for a disabled automation, want 0", spawn.createCallCount())
	}
}

// TestAutomationSessionNameUsesReservedScheme pins
// automationSessionName's D11 save-name scheme directly.
func TestAutomationSessionNameUsesReservedScheme(t *testing.T) {
	got := automationSessionName("nightly-summary", "run-123")
	want := "__auto__nightly-summary__run-123"
	if got != want {
		t.Fatalf("automationSessionName = %q, want %q", got, want)
	}
	if chat.IsAutoSaveName(got) {
		t.Fatalf("automationSessionName %q collides with chat.AutoSaveName's prefix check", got)
	}
}

// TestOriginForTriggerScheduled pins originForTrigger's TriggerScheduled
// branch directly - every other test in this file only ever passes
// ports.TriggerManual through RunOnce, so the "scheduled" string branch
// had no direct coverage.
func TestOriginForTriggerScheduled(t *testing.T) {
	if got := originForTrigger(ports.TriggerScheduled); got != "scheduled" {
		t.Fatalf("originForTrigger(TriggerScheduled) = %q, want %q", got, "scheduled")
	}
	if got := originForTrigger(ports.TriggerManual); got != "manual" {
		t.Fatalf("originForTrigger(TriggerManual) = %q, want %q", got, "manual")
	}
}

// TestTurnTimeoutUsesConfiguredValue pins turnTimeout's configured
// (non-default) branch: every other test in this file builds its Service
// with Config{} (zero-value TurnTimeout), so only the defaultTurnTimeout
// fallback branch had coverage.
func TestTurnTimeoutUsesConfiguredValue(t *testing.T) {
	svc, err := New(t.TempDir(), nil, nil, Config{TurnTimeout: 42 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc.turnTimeout(); got != 42*time.Second {
		t.Fatalf("turnTimeout() with configured value = %v, want 42s", got)
	}
}

// TestFindSpecPropagatesLoadError covers findSpec's own error-wrap
// branch: LoadSpecs fails when automations.toml exists but is malformed
// TOML, distinct from the "automation not found" 404 case every other
// findSpec-exercising test (via RunOnce) hits.
func TestFindSpecPropagatesLoadError(t *testing.T) {
	root := t.TempDir()
	path, err := automationsFilePath(ports.ScopeProject, root)
	if err != nil {
		t.Fatalf("automationsFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not [valid toml"), 0o644); err != nil {
		t.Fatalf("write malformed automations.toml: %v", err)
	}
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.findSpec("anything"); err == nil {
		t.Fatal("findSpec against malformed automations.toml: got nil error, want a load-wrap error")
	}
}

// TestRecordSkippedRunPropagatesStoreError drops the automation_runs
// table (keeping run_claims intact) before a lost-claim fire, so
// admitFire's own claim win still succeeds but recordSkippedRun's
// createRun call fails - distinct from every real dedup test, which
// always has a fully live, writable store for both tables.
func TestRecordSkippedRunPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, ok, err := svc.admitFire(ctx, automationID); err != nil || !ok {
		t.Fatalf("pre-acquire admitFire: ok=%v err=%v", ok, err)
	}
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	if _, err := svc.RunOnce(ctx, automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce with a lost claim and automation_runs dropped: got nil error, want recordSkippedRun's own store error")
	}
}

// TestRunOnceStartRunPropagatesCreateRunError covers startRun's own
// createRun error-wrap branch by dropping automation_runs (keeping
// run_claims intact) AFTER a real admitFire win, so RunOnce proceeds
// past admitFire and reaches startRun's createRun call, which then
// fails against the missing table.
func TestRunOnceStartRunPropagatesCreateRunError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := dropAutomationRunsTable(t, db); err != nil {
		t.Fatalf("drop automation_runs table: %v", err)
	}
	if _, err := svc.RunOnce(ctx, automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce with automation_runs dropped: got nil error, want startRun's own createRun error")
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0 (startRun must fail before any session spawns)", spawn.createCallCount())
	}
}

// TestCreateRunWorktreePropagatesConfigLoadError covers
// createRunWorktree's own config-load error-wrap branch (distinct from
// TestRunOnceWorktreeCreationFailureNoSessionNoOrphan, which reaches
// cliworktree.CreateManagedWorktree's own failure, past a successful
// config load): a malformed mivia.toml under root makes
// config.LoadWorktreeConfig itself fail before CreateManagedWorktree is
// ever called.
func TestCreateRunWorktreePropagatesConfigLoadError(t *testing.T) {
	root := t.TempDir()
	miviaPath := filepath.Join(root, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(miviaPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(miviaPath, []byte("not [valid toml"), 0o644); err != nil {
		t.Fatalf("write malformed mivia.toml: %v", err)
	}
	svc, err := New(root, nil, nil, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := svc.createRunWorktree(Spec{ID: "auto-x", BaseRef: "HEAD"}, "run-x"); err == nil {
		t.Fatal("createRunWorktree with a malformed mivia.toml: got nil error, want a config-load-wrap error")
	}
}

// TestRunStepUnknownKindRejected covers runStep's own default branch: a
// Step whose Kind is outside the five declared values is rejected by
// name rather than silently dispatched as StepPrompt (the zero value).
func TestRunStepUnknownKindRejected(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, Step{Kind: StepKind(99)}, time.Second)
	if err == nil {
		t.Fatal("runStep with an unknown Kind: got nil error, want rejection")
	}
}

// TestRunStepWorkflowPropagatesEngineStartError covers runStep's
// StepWorkflow case and runWorkflowStep's own Start-error-wrap branch:
// dispatching a workflow name that has no on-disk definition fails
// deterministically through the real cliworkflow engine, without needing
// a fully wired workflow fixture.
//
// cliworkflow.PrepareWorkflowRun calls two package-level seams
// (ApplyPrivacyPolicyFunc, OpenContextStoreFunc) that
// internal/cli/cliworkflow_wiring.go's init() assigns in the real binary
// (cmd/mivia imports internal/cli directly, so that init() always runs
// before internal/newtui's wireAutomationBackend ever constructs a live
// automation.Service - see docs/design/automations.md D12/D4). This test
// package imports internal/automation directly, bypassing that init()
// chain entirely, so both seams are nil here and must be wired locally
// exactly as internal/cliworkflow's own testmain_test.go does, or the
// call panics on a nil func value rather than exercising the intended
// error path.
func TestRunStepWorkflowPropagatesEngineStartError(t *testing.T) {
	prevApplyPrivacy := cliworkflow.ApplyPrivacyPolicyFunc
	prevOpenStore := cliworkflow.OpenContextStoreFunc
	cliworkflow.ApplyPrivacyPolicyFunc = func(*config.Resolved) {}
	cliworkflow.OpenContextStoreFunc = func(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
		return storage.OpenSQLite(filepath.Join(t.TempDir(), "workflow-step.db"))
	}
	t.Cleanup(func() {
		cliworkflow.ApplyPrivacyPolicyFunc = prevApplyPrivacy
		cliworkflow.OpenContextStoreFunc = prevOpenStore
	})

	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{Kind: StepWorkflow, Ref: "no-such-workflow-definition"}
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepWorkflow) against an undefined workflow: got nil error, want the engine's Start error")
	}
	if !strings.Contains(err.Error(), "no-such-workflow-definition") {
		t.Fatalf("runStep(StepWorkflow) error = %q, want it naming the workflow ref", err.Error())
	}
}

type fakeWorkflowRunHandler struct {
	completer provider.Completer
	model     string
}

func (h fakeWorkflowRunHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if h.completer == nil {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if _, err := h.completer.Chat(ctx, provider.Request{Model: h.model, Messages: []provider.Message{{Role: "user", Content: string(req.Input)}}}); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// wireWorkflowStepSuccessSeams wires every cliworkflow package-level seam
// TestRunStepWorkflowSucceedsOnEngineStart needs to drive a StepWorkflow
// dispatch through the real engine to a successful Start, and registers
// their restore via t.Cleanup. Factored out of the test itself to keep
// that function under the project's per-function LOC limit; see the
// test's own doc comment for why each seam is needed.
func wireWorkflowStepSuccessSeams(t *testing.T) {
	t.Helper()
	prevApplyPrivacy := cliworkflow.ApplyPrivacyPolicyFunc
	prevOpenStore := cliworkflow.OpenContextStoreFunc
	prevContextStorePath := cliworkflow.ContextStorePath
	prevHooks := cliworkflow.WorkflowExecutionHooks
	prevInstallHooks := cliworkflow.InstallHookSessionFunc
	prevLoadSkills := cliworkflow.WorkflowBuildLoadSkills
	prevDispatcher := cliworkflow.WorkflowBuildDispatcher
	prevSliceErrors := cliworkflow.SliceErrorsFunc
	prevInitCoordinator := cliworkflow.InitCoordinatorFunc
	prevAutoDeliveryLoop := cliworkflow.SessionAutoDeliveryRepairLoopFunc

	cliworkflow.ApplyPrivacyPolicyFunc = func(*config.Resolved) {}
	cliworkflow.InitCoordinatorFunc = func(d *runtime.Dispatcher, cfg config.SubagentConfig, repos ...ledger.LedgerRepository) *coordinator.Coordinator {
		return coordinator.New(repos[0], subagents.New(d, subagents.Policy{Workers: 4}))
	}
	// LaunchStartedWorkflow launches the actual run/repair loop on a
	// background goroutine after Start returns; this test only asserts
	// runStep's own Start-succeeded return, so the loop is stubbed to a
	// no-op exactly like TestSessionLaunchResumeReadFailure
	// (workflow_coverage_pass3_test.go) does - running the real loop here
	// would race this test's own t.Cleanup/TempDir teardown.
	cliworkflow.SessionAutoDeliveryRepairLoopFunc = func(context.Context, workflowledger.Repository, string, *config.Resolved, *storage.SQLite, string, func(context.Context) (workflowledger.RunSnapshot, error), func(context.Context) (bool, error), bool) {
	}
	cliworkflow.SliceErrorsFunc = func(context string, errs []string) error {
		if len(errs) == 0 {
			return nil
		}
		return fmt.Errorf("%s: %s", context, strings.Join(errs, "; "))
	}
	cliworkflow.ContextStorePath = func(root string, cfg config.SubagentConfig) string {
		return filepath.Join(root, "workflow-step.db")
	}
	cliworkflow.OpenContextStoreFunc = func(root string, cfg config.SubagentConfig) (*storage.SQLite, error) {
		return storage.OpenSQLite(filepath.Join(root, "workflow-step.db"))
	}
	cliworkflow.InstallHookSessionFunc = func(string, bool, bool) (func(), error) { return func() {}, nil }
	cliworkflow.WorkflowExecutionHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	cliworkflow.WorkflowBuildLoadSkills = func(string) (*skills.Registry, error) {
		return skills.NewRegistry(), nil
	}
	cliworkflow.WorkflowBuildDispatcher = func(opts cliagents.SessionDispatcherOpts) (*runtime.Dispatcher, error) {
		d := runtime.New(runtime.Policy{})
		if opts.AgentRegistry != nil {
			for _, agent := range opts.AgentRegistry.List() {
				_ = d.Register(runtime.Subagent, agent.Name, fakeWorkflowRunHandler{completer: opts.Completer, model: opts.Model})
			}
		}
		return d, nil
	}
	cliworkflow.InitCLIDefaults()

	t.Cleanup(func() {
		cliworkflow.ApplyPrivacyPolicyFunc = prevApplyPrivacy
		cliworkflow.OpenContextStoreFunc = prevOpenStore
		cliworkflow.ContextStorePath = prevContextStorePath
		cliworkflow.WorkflowExecutionHooks = prevHooks
		cliworkflow.InstallHookSessionFunc = prevInstallHooks
		cliworkflow.WorkflowBuildLoadSkills = prevLoadSkills
		cliworkflow.WorkflowBuildDispatcher = prevDispatcher
		cliworkflow.SliceErrorsFunc = prevSliceErrors
		cliworkflow.InitCoordinatorFunc = prevInitCoordinator
		cliworkflow.SessionAutoDeliveryRepairLoopFunc = prevAutoDeliveryLoop
	})
}

// writeWorkflowStepSuccessFixture writes a minimal, valid single-step
// agent workflow (named "solo") plus its config, agent definition,
// template, and output schema under a fresh t.TempDir(), pointed at
// serverURL as the provider base_url. Returns the workspace root.
// Factored out of TestRunStepWorkflowSucceedsOnEngineStart to keep that
// function under the project's per-function LOC limit.
func writeWorkflowStepSuccessFixture(t *testing.T, serverURL string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	t.Setenv("WORKFLOW_TEST_KEY", "test-key")

	workflowRoot := filepath.Join(root, ".mivia", "workflows")
	for _, dir := range []string{
		filepath.Join(workflowRoot, "templates"),
		filepath.Join(workflowRoot, "schemas"),
		filepath.Join(root, ".agents", "agents"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfgContent := `[provider]
name = "openrouter"

[providers.openrouter]
base_url = "` + serverURL + `"
api_key_env = "WORKFLOW_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]

[subagents]
max_workers = 1
default_timeout_seconds = 30
`
	writeFile(filepath.Join(root, ".mivia", "mivia.toml"), cfgContent)
	writeFile(filepath.Join(root, ".agents", "agents", "one.md"), "---\nname: one\ndescription: test\ntools: [read_file]\nmax_turns: 1\n---\n")
	writeFile(filepath.Join(workflowRoot, "templates", "one.md"), "Return the result for {{ inputs.task }}.")
	writeFile(filepath.Join(workflowRoot, "schemas", "out.json"), `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}},"additionalProperties":false}`)
	writeFile(filepath.Join(workflowRoot, "solo.toml"), `version = 1
name = "solo"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"
template = "templates/one.md"
output_schema = "schemas/out.json"
context = [{ from = "inputs.task", as = "task", max_bytes = 100 }]

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`)
	return root
}

// TestRunStepWorkflowSucceedsOnEngineStart covers runStep's StepWorkflow
// case and runWorkflowStep's own SUCCESS return (executor.go's line
// immediately after eng.Start succeeds): dispatching a real, valid
// single-step workflow through the real cliworkflow engine, with every
// package-level seam it needs wired locally (see
// wireWorkflowStepSuccessSeams), completes runStep with a nil error.
func TestRunStepWorkflowSucceedsOnEngineStart(t *testing.T) {
	wireWorkflowStepSuccessSeams(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`))
	}))
	t.Cleanup(server.Close)

	root := writeWorkflowStepSuccessFixture(t, server.URL)

	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{
		Kind:   StepWorkflow,
		Ref:    "solo",
		Inputs: map[string]string{"task": "test-task"},
	}
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err != nil {
		t.Fatalf("runStep(StepWorkflow) unexpected error: %v", err)
	}
}

var _ = filepath.Join // silence unused import if filepath's only other use is removed later

// TestSpawnRunSessionPropagatesApprovalOverrideError covers
// spawnRunSession's own SetApprovalOverride-failure branch: the spawned
// session's D8 override cannot be installed, so RunOnce must abort the
// run RunFailed BEFORE any step is dispatched - a failure to establish
// the unattended posture must never fall through to running steps under
// an unknown/inherited posture.
func TestSpawnRunSessionPropagatesApprovalOverrideError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation(), setApprovalErr: fmt.Errorf("override install failed")}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: got error %v, want nil (failure is recorded on the run)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed", run.State)
	}
	if !strings.Contains(run.Message, "override install failed") {
		t.Fatalf("run.Message = %q, want it to name the override install failure", run.Message)
	}
	if len(spawn.conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a failed approval override must precede any step dispatch)", spawn.conv.sentTexts())
	}
}

// TestRunOnceAdmitFirePropagatesRealError covers RunOnce's own
// admitFire-error-wrap-and-return branch: dropping run_claims before
// calling RunOnce makes admitFire fail with a real, non-ErrClaimHeld
// error, distinct from the documented lost-claim no-op every dedup test
// exercises.
func TestRunOnceAdmitFirePropagatesRealError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, nil)
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := dropRunClaimsTable(t, db); err != nil {
		t.Fatalf("drop run_claims table: %v", err)
	}
	if _, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce with run_claims dropped: got nil error, want admitFire's own real error")
	}
	if spawn.createCallCount() != 0 {
		t.Fatalf("CreateFreshInDir called %d times, want 0", spawn.createCallCount())
	}
}

// TestStartRunPropagatesUpdateRunStateError covers startRun's own
// second error-wrap branch directly (the pending->running
// updateRunState call): a SQLite trigger makes every UPDATE on
// automation_runs fail while INSERTs still succeed, so startRun's own
// createRun call succeeds but its immediately-following updateRunState
// call fails - the two calls are synchronous with no seam between them
// to interleave an out-of-band row mutation, so the trigger is the only
// way to fail the second write specifically.
func TestStartRunPropagatesUpdateRunStateError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := forceAutomationRunsUpdateFailures(t, db); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}
	spec := Spec{ID: "auto-startrun-err", Steps: []Step{{Kind: StepPrompt, Prompt: "x"}}}
	if _, err := svc.startRun(context.Background(), spec, "auto-startrun-err", ports.TriggerManual, "holder-x"); err == nil {
		t.Fatal("startRun with automation_runs UPDATEs forced to fail: got nil error, want the second updateRunState error wrapped")
	}
}

// TestRunStepsPropagatesCheckpointError covers runSteps' own checkpoint
// updateRunState-error-wrap branch: the fake conversation drops the
// automation_runs table as a side effect of successfully completing its
// one step's Send call, so runStep returns nil (the step itself
// "succeeded") but the immediately following checkpoint updateRunState
// call - still inside runSteps, before it ever returns to RunOnce -
// fails against the now-missing table.
func TestRunStepsPropagatesCheckpointError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	conv := newRecordingConversation()
	conv.onSend = func() { _ = dropAutomationRunsTable(t, db) }
	spawn := &fakeExecSpawner{conv: conv}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	run, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual)
	if err != nil {
		t.Fatalf("RunOnce: got error %v, want nil (checkpoint failure is recorded via failRun, not returned)", err)
	}
	if run.State != ports.RunFailed {
		t.Fatalf("RunOnce State = %v, want RunFailed (checkpoint failure after a successful step)", run.State)
	}
	if !strings.Contains(run.Message, "checkpoint") {
		t.Fatalf("run.Message = %q, want it to name the checkpoint failure", run.Message)
	}
}

// TestRunOnceMarkSucceededPropagatesStoreError covers RunOnce's own
// final "mark run succeeded" error-wrap branch: a SQLite trigger allows
// the first two automation_runs UPDATEs (startRun's own pending->running
// transition, then the one step's own checkpoint) to succeed for real,
// then fails every UPDATE after that - so the run reaches a genuinely
// completed steps loop before RunOnce's own separate, final "mark
// succeeded" call is the one that hits the trigger.
func TestRunOnceMarkSucceededPropagatesStoreError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	automationID := seedEnabledAutomation(t, root, func(s *Spec) {
		s.Steps = []Step{{Kind: StepPrompt, Prompt: "only step"}}
	})
	if err := forceAutomationRunsUpdateFailuresAfter(t, db, 2); err != nil {
		t.Fatalf("install update-failing trigger: %v", err)
	}
	spawn := &fakeExecSpawner{conv: newRecordingConversation()}
	svc, err := New(root, db, spawn, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.RunOnce(context.Background(), automationID, ports.TriggerManual); err == nil {
		t.Fatal("RunOnce whose final mark-succeeded UPDATE is forced to fail: got nil error, want the wrapped store error")
	}
}

// TestRunStepSlashRejectedByExecutionTimeValidation covers runStep's
// StepSlash case's own validateStepSlash rejection branch, directly:
// re-validation at execution time (defense in depth) refuses a
// D15-rejected command before ever calling sendTurnHeadless.
func TestRunStepSlashRejectedByExecutionTimeValidation(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{Kind: StepSlash, Ref: "/delete"} // session-lifecycle mutation, D15-rejected
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepSlash) with a D15-rejected command: got nil error, want rejection")
	}
	if len(conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a rejected slash command must never reach sendTurnHeadless)", conv.sentTexts())
	}
}

// TestRunStepAgentPropagatesApplySessionAgentError covers runStep's
// StepAgent case's own ApplySessionAgent error-wrap branch: a nil
// *chat.Session (what this test's dispatch always has, since this
// package's Service carries no real session-construction path in a unit
// test - see this file's own documented gap on StepAgent) makes
// ApplySessionAgent fail immediately and deterministically, before ever
// calling sendTurnHeadless.
func TestRunStepAgentPropagatesApplySessionAgentError(t *testing.T) {
	root := t.TempDir()
	db := newTestDB(t)
	svc, err := New(root, db, &fakeExecSpawner{conv: newRecordingConversation()}, Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conv := newRecordingConversation()
	step := Step{Kind: StepAgent, Ref: "some-agent", Prompt: "do the thing"}
	err = svc.runStep(context.Background(), "auto-x", "run-x", 0, root, conv, nil, step, time.Second)
	if err == nil {
		t.Fatal("runStep(StepAgent) with a nil session: got nil error, want ApplySessionAgent's own rejection")
	}
	if len(conv.sentTexts()) != 0 {
		t.Fatalf("sent texts = %v, want none (a failed agent selection must never reach sendTurnHeadless)", conv.sentTexts())
	}
}
