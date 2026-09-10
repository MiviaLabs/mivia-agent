package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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
}

func newRecordingConversation() *recordingConversation {
	return &recordingConversation{}
}

func (c *recordingConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.sent = append(c.sent, in.Text)
	n := len(c.sent)
	c.mu.Unlock()
	if c.failAt > 0 && n == c.failAt {
		if c.failWith != nil {
			return nil, c.failWith
		}
		return nil, fmt.Errorf("recordingConversation: forced failure at call %d", n)
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
	mu          sync.Mutex
	conv        *recordingConversation
	createCalls int
	createErr   error
	overrides   []approvalOverride
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

var _ = filepath.Join // silence unused import if filepath's only other use is removed later
