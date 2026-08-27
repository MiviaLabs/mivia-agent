package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// StepRuntime contains snapshotted data required to execute one agent step.
type StepRuntime struct {
	Agent        agents.ResolvedAgent
	Digest       string
	ProviderName string
	Model        string
	Template     string
	Schema       map[string]any
}

// Admission contains immutable host data for one workflow run.
type Admission struct {
	InvocationKey    string
	BaseRef          string
	BaseCommit       string
	OriginBaseCommit string
	WorktreeName     string
	InputDigest      string
	DeadlineAt       *time.Time
	RemoteURL        string
	// WorkflowDigest is the digest RECORDED when the run was admitted. A
	// resume must pass it; a fresh admission leaves it empty.
	//
	// The digest is a hash of the marshalled definition struct, so it moves
	// whenever those types gain a field, even when the workflow text does not
	// change by one byte. Comparing a resumed run against a digest THIS binary
	// recomputed therefore asserts that this binary hashes the definition the
	// way the admitting binary did, which is a fact about the binary. Two
	// field additions moved it in one day, and every run admitted before them
	// became permanently unresumable.
	//
	// The definition text is proven by other means on the resume path: the
	// snapshot digest covers the raw snapshot bytes, StartNew compares those
	// bytes directly, and the two recorded digests are compared to each other.
	WorkflowDigest string
}

// LinearController advances a workflow one active step at a time.
// Phase 4 supports agent, agent_gate, evidence_gate, human_gate, and loops.
type LinearController struct {
	Repo           workflowledger.Repository
	Runner         AgentStepRunner
	Workflow       *definition.CompiledWorkflow
	Steps          map[string]StepRuntime
	Inputs         map[string]any
	RunID          string
	Snapshot       []byte
	Holder         string
	Verifiers      *definition.Catalogue
	WorkDir        string
	ModuleBaseline *definition.GoModuleBaseline
	SecretPolicy   secretpath.Policy
	// gitRunner/gitCtx pin the worktree git context for the diff-size gate.
	gitRunner delivery.GitRunner
	gitCtx    delivery.GitContext
	// WritePathBlocklist is the host write-path denylist for workflow agents
	// (internal/tools enforced): paths under it can never be written by an
	// agent step. The controller uses it to recognize a succeeded step whose
	// output admits a write it cannot perform (blocked_paths, a claimed
	// files_changed entry, or a review finding demanding a blocked edit) and
	// fail the run honestly instead of looping it into review.
	WritePathBlocklist []string
	PanelLimiter       *PanelActorLimiter
	// PanelLimits bounds every agent_panel step's member and synthesis
	// children (panel_attempt.go/panel_synthesis.go). Defaults to
	// DefaultPanelLimits(); a host overrides it via SetPanelLimits
	// before Start, resolved from [workflows.panels] config.
	PanelLimits       PanelLimits
	progress          ProgressSink
	admission         Admission
	forceResume       bool
	now               func() time.Time
	started           bool
	mu                sync.Mutex
	heartbeatThrottle *durableHeartbeatThrottle
}

// NewLinearController creates a controller for an admitted workflow run.
func NewLinearController(repo workflowledger.Repository, runner AgentStepRunner, wf *definition.CompiledWorkflow, steps map[string]StepRuntime, inputs map[string]any, runID string, snapshot []byte) (*LinearController, error) {
	if repo == nil || runner == nil || wf == nil {
		return nil, fmt.Errorf("linear controller dependencies are incomplete")
	}
	if strings.TrimSpace(runID) == "" {
		runID = newWorkflowRunID()
	}
	if !strings.HasPrefix(runID, "wfr-") {
		return nil, fmt.Errorf("workflow run ID must start with wfr-")
	}
	if len(snapshot) == 0 {
		return nil, fmt.Errorf("workflow snapshot is empty")
	}
	admitted, steps, err := admitStackingRun(wf, steps, inputs)
	if err != nil {
		return nil, err
	}
	return &LinearController{Repo: repo, Runner: runner, Workflow: admitted, Steps: steps, Inputs: cloneValues(inputs), RunID: runID, Snapshot: append([]byte(nil), snapshot...), Holder: newWorkflowHolder(), now: time.Now, PanelLimiter: NewPanelActorLimiter(), PanelLimits: DefaultPanelLimits(), heartbeatThrottle: newDurableHeartbeatThrottle()}, nil
}

// SetPanelLimits overrides the compiled PanelLimits defaults before
// Start. A host resolves this from [workflows.panels] config
// (config.WorkflowsConfig.Panels), falling back to
// DefaultPanelLimits() for any unset field before calling this.
func (c *LinearController) SetPanelLimits(limits PanelLimits) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.PanelLimits = limits
	return nil
}

// SetProgressSink sets the workflow progress sink before Start.
func (c *LinearController) SetProgressSink(sink ProgressSink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.progress = sink
	return nil
}

// SetVerifiers sets the host verifier catalogue before Start.
func (c *LinearController) SetVerifiers(cat *definition.Catalogue) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.Verifiers = cat
	return nil
}

// SetSecretPolicy sets the secret-exclusion policy for sandboxed evidence
// gate commands before Start.
func (c *LinearController) SetSecretPolicy(policy secretpath.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.SecretPolicy = policy
	return nil
}

// SetWritePathBlocklist sets the host write-path denylist for workflow agent
// steps before Start. Entries must be non-empty workspace-relative paths;
// validation rejects absolute paths and entries that normalize to nothing.
func (c *LinearController) SetWritePathBlocklist(paths []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	blocklist := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("write path blocklist entry is empty")
		}
		if path.IsAbs(p) {
			return fmt.Errorf("write path blocklist entry %q is absolute; entries must be workspace-relative", p)
		}
		blocklist = append(blocklist, p)
	}
	c.WritePathBlocklist = blocklist
	return nil
}

// SetWorkDir sets the workspace directory for evidence_gate host checks.
func (c *LinearController) SetWorkDir(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.WorkDir = dir
	return nil
}

// SetModuleBaseline sets immutable Go module inputs before Start.
func (c *LinearController) SetModuleBaseline(baseline *definition.GoModuleBaseline) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	if baseline == nil || len(baseline.GoMod) == 0 {
		return fmt.Errorf("workflow verifier module baseline is empty")
	}
	c.ModuleBaseline = &definition.GoModuleBaseline{GoMod: append([]byte(nil), baseline.GoMod...), GoSum: append([]byte(nil), baseline.GoSum...)}
	return nil
}

// SetAdmission sets immutable host admission data before Start.
func (c *LinearController) SetAdmission(admission Admission) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.admission = admission
	return nil
}

// SetForceResume sets explicit claim recovery before Start.
func (c *LinearController) SetForceResume(force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.forceResume = force
	return nil
}

// Start admits the run. It is idempotent for the same run ID and snapshot.
func (c *LinearController) Start(ctx context.Context) error {
	_, err := c.StartNew(ctx)
	return err
}

// StartNew admits the run and reports whether this controller created it.
// A false result means another executor already admitted the same snapshot.
func (c *LinearController) StartNew(ctx context.Context) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("linear controller is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return false, nil
	}
	snap := c.admissionSnapshot()
	created := true
	if err := c.Repo.CreateRun(ctx, snap, c.Snapshot); err != nil {
		if !errors.Is(err, workflowledger.ErrDuplicate) && !errors.Is(err, workflowledger.ErrConflict) && !errors.Is(err, workflowledger.ErrClaimHeld) {
			return false, err
		}
		created = false
		stored, getErr := c.Repo.GetRunSnapshot(ctx, c.RunID)
		if getErr != nil {
			return false, getErr
		}
		if !bytes.Equal(stored, c.Snapshot) {
			return false, fmt.Errorf("workflow run %q already exists with a different snapshot", c.RunID)
		}
		run, getRunErr := c.Repo.GetRun(ctx, c.RunID)
		if getRunErr != nil {
			return false, getRunErr
		}
		if !sameAdmission(run, snap) {
			return false, fmt.Errorf("workflow run %q already exists with different admission data", c.RunID)
		}
		// Record the resume on the audit trail. The record is best-effort:
		// a failure must not fail StartNew.
		if err := c.Repo.RecordRunResumed(ctx, c.RunID); err != nil {
			log.Printf("workflow run %q resume record failed: %v", c.RunID, err)
		}
	}
	c.started = true
	return created, nil
}

// Run advances until the run reaches a terminal status.
func (c *LinearController) Run(ctx context.Context) (workflowledger.RunSnapshot, error) {
	if err := c.Start(ctx); err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	ctx = workflowledger.ContextWithRunID(ctx, c.RunID)
	stored, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	if stored.DeadlineAt != nil {
		remaining := stored.DeadlineAt.Sub(c.now())
		if remaining <= 0 {
			return c.timeoutExpiredRun(ctx, stored)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	for {
		snap, done, err := c.Advance(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				writeCtx = workflowledger.ContextWithClaimHolder(writeCtx, c.Holder)
				if claimErr := c.Repo.ClaimRun(writeCtx, c.RunID, c.Holder); claimErr != nil {
					cancel()
					return snap, err
				}
				defer func() { _ = c.Repo.ReleaseRun(context.Background(), c.RunID, c.Holder) }()
				current, getErr := c.Repo.GetRun(writeCtx, c.RunID)
				if getErr == nil {
					settled, terminal, settleErr := c.reconcileTerminalRoute(writeCtx, current)
					if terminal {
						cancel()
						return settled, settleErr
					}
					if !workflowledger.IsTerminalRunStatus(current.Status) {
						_, _, _ = c.failWithStatus(writeCtx, current, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
					}
				}
				cancel()
			}
			return snap, err
		}
		if done {
			// A terminal run emits exactly one run_finished. Non-terminal parks
			// (waiting_approval, delivery_pending) are pauses, not finishes:
			// no event is emitted until the run reaches a terminal status.
			if workflowledger.IsTerminalRunStatus(snap.Status) {
				c.emitRunFinished(string(snap.Status))
			}
			return snap, nil
		}
		if !panelsEnabled {
			if step, ok := c.WorkflowStep(snap.ActiveStepID); ok && step.Kind == "agent_panel" {
				// Wave 4 completes the member phase only. Wave 5 advances the
				// persisted panel phase into synthesis.
				return snap, ErrPanelMembersComplete
			}
		}
	}
}

// Advance executes the current step once. It returns done when the run is terminal.
func (c *LinearController) Advance(ctx context.Context) (workflowledger.RunSnapshot, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx = workflowledger.ContextWithRunID(ctx, c.RunID)
	ctx = workflowledger.ContextWithClaimHolder(ctx, c.Holder)
	if err := c.Repo.ClaimRun(ctx, c.RunID, c.Holder); err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	defer func() { _ = c.Repo.ReleaseRun(context.Background(), c.RunID, c.Holder) }()
	stepCtx, cancelStep := context.WithCancel(ctx)
	defer cancelStep()
	defer c.startClaimHeartbeat(cancelStep)()
	ctx = stepCtx
	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return run, true, nil
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return run, true, nil
	}
	if run.Status == workflowledger.RunStatusPending {
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
			return run, false, err
		}
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return run, false, err
		}
	}
	if settled, terminal, settleErr := c.reconcileTerminalRoute(ctx, run); terminal {
		return settled, true, settleErr
	}
	if run.DeadlineAt != nil && !c.now().Before(*run.DeadlineAt) {
		settled, timeoutErr := c.timeoutExpiredRun(ctx, run)
		return settled, true, timeoutErr
	}
	if run.Status == workflowledger.RunStatusWaitingApproval {
		// Finish partial Approve/Reject, or remain paused until operator action.
		return c.reconcileWaitingApproval(ctx, run)
	}
	step, ok := c.WorkflowStep(run.ActiveStepID)
	if !ok {
		return c.fail(ctx, run, fmt.Errorf("workflow step %q is not declared", run.ActiveStepID))
	}
	switch step.Kind {
	case "agent", "agent_gate":
		runtime, ok := c.Steps[step.ID]
		if !ok {
			return c.fail(ctx, run, fmt.Errorf("step %q has no snapshotted runtime", step.ID))
		}
		return c.advanceAgentStep(ctx, run, step, runtime)
	case "agent_panel":
		return c.advancePanelStep(ctx, run, step)
	case "evidence_gate":
		return c.advanceEvidenceGate(ctx, run, step)
	case "human_gate":
		return c.advanceHumanGate(ctx, run, step)
	default:
		return c.fail(ctx, run, fmt.Errorf("unsupported step kind %q on step %q", step.Kind, step.ID))
	}
}

func (c *LinearController) WorkflowStep(id string) (definition.Step, bool) {
	for _, step := range c.Workflow.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return definition.Step{}, false
}

// fail settles the run as Failed via the shared terminal-status path.
func (c *LinearController) fail(ctx context.Context, run workflowledger.RunSnapshot, cause error) (workflowledger.RunSnapshot, bool, error) {
	return c.failWithStatus(ctx, run, cause, workflowledger.RunStatusFailed)
}

func (c *LinearController) failWithStatus(ctx context.Context, run workflowledger.RunSnapshot, cause error, status workflowledger.RunStatus) (workflowledger.RunSnapshot, bool, error) {
	if !workflowledger.IsTerminalRunStatus(run.Status) {
		c.emitRunFailed(cause.Error())
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, status, nil); err != nil {
			return run, false, fmt.Errorf("workflow failed: %v: %w", cause, err)
		}
	}
	failed, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return run, false, fmt.Errorf("workflow failed: %v: %w", cause, err)
	}
	return failed, true, fmt.Errorf("workflow step failed: %w", cause)
}
