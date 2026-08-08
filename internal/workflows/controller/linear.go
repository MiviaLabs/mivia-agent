package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/verifier"
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
	Workflow       *compiler.CompiledWorkflow
	Steps          map[string]StepRuntime
	Inputs         map[string]any
	RunID          string
	Snapshot       []byte
	Holder         string
	Verifiers      *verifier.Catalogue
	WorkDir        string
	ModuleBaseline *verifier.GoModuleBaseline
	SecretPolicy   secretpath.Policy
	PanelLimiter   *PanelActorLimiter
	admission      Admission
	forceResume    bool
	now            func() time.Time
	started        bool
	mu             sync.Mutex
}

// NewLinearController creates a controller for an admitted workflow run.
func NewLinearController(repo workflowledger.Repository, runner AgentStepRunner, wf *compiler.CompiledWorkflow, steps map[string]StepRuntime, inputs map[string]any, runID string, snapshot []byte) (*LinearController, error) {
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
	return &LinearController{Repo: repo, Runner: runner, Workflow: wf, Steps: steps, Inputs: cloneValues(inputs), RunID: runID, Snapshot: append([]byte(nil), snapshot...), Holder: newWorkflowHolder(), now: time.Now, PanelLimiter: NewPanelActorLimiter()}, nil
}

// SetVerifiers sets the host verifier catalogue before Start.
func (c *LinearController) SetVerifiers(cat *verifier.Catalogue) error {
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
func (c *LinearController) SetModuleBaseline(baseline *verifier.GoModuleBaseline) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	if baseline == nil || len(baseline.GoMod) == 0 {
		return fmt.Errorf("workflow verifier module baseline is empty")
	}
	c.ModuleBaseline = &verifier.GoModuleBaseline{GoMod: append([]byte(nil), baseline.GoMod...), GoSum: append([]byte(nil), baseline.GoSum...)}
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
	}
	c.started = true
	return created, nil
}

func (c *LinearController) admissionSnapshot() workflowledger.RunSnapshot {
	admittedAt := c.now()
	snap := workflowledger.RunSnapshot{
		RunID: c.RunID, InvocationKey: c.admission.InvocationKey, WorkflowName: c.Workflow.Name, WorkflowDigest: c.admittedWorkflowDigest(),
		SnapshotDigest: workflowledger.SnapshotDigest(c.Snapshot), InputDigest: c.admission.InputDigest,
		Status: workflowledger.RunStatusPending, ActiveStepID: c.Workflow.InitialStep,
		BaseRef: c.admission.BaseRef, BaseCommit: c.admission.BaseCommit,
		OriginBaseCommit: c.admission.OriginBaseCommit, WorktreeName: c.admission.WorktreeName,
		RemoteURL: c.admission.RemoteURL, StartedAt: admittedAt,
	}
	if c.admission.DeadlineAt != nil {
		deadline := *c.admission.DeadlineAt
		snap.DeadlineAt = &deadline
	} else if c.Workflow.Limits.MaxDurationSeconds > 0 {
		deadline := admittedAt.Add(time.Duration(c.Workflow.Limits.MaxDurationSeconds) * time.Second)
		snap.DeadlineAt = &deadline
	}
	return snap
}

// admittedWorkflowDigest returns the digest to compare against the stored run:
// the recorded one for a resume, this binary's for a fresh admission. A fresh
// admission keeps the full guard, so two concurrent admissions of different
// workflows still collide.
func (c *LinearController) admittedWorkflowDigest() string {
	if c.admission.WorkflowDigest != "" {
		return c.admission.WorkflowDigest
	}
	return c.Workflow.Digest
}

func sameAdmission(stored, candidate workflowledger.RunSnapshot) bool {
	return stored.InvocationKey == candidate.InvocationKey && stored.WorkflowName == candidate.WorkflowName && stored.WorkflowDigest == candidate.WorkflowDigest &&
		stored.SnapshotDigest == candidate.SnapshotDigest && stored.InputDigest == candidate.InputDigest &&
		stored.BaseRef == candidate.BaseRef && stored.BaseCommit == candidate.BaseCommit &&
		stored.OriginBaseCommit == candidate.OriginBaseCommit &&
		stored.WorktreeName == candidate.WorktreeName && stored.RemoteURL == candidate.RemoteURL &&
		sameDeadline(stored.DeadlineAt, candidate.DeadlineAt)
}

func sameDeadline(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
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
			return snap, nil
		}
		if step, ok := c.WorkflowStep(snap.ActiveStepID); ok && step.Kind == "agent_panel" {
			// Wave 4 completes the member phase only. Wave 5 advances the
			// persisted panel phase into synthesis.
			return snap, nil
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

func (c *LinearController) contextForStep(ctx context.Context, step definition.Step, attempts []workflowledger.StepAttempt) (map[string]any, map[string]any, map[string]ArtifactRef, error) {
	inputs := make(map[string]any)
	evidence := make(map[string]any)
	refs := make(map[string]ArtifactRef)
	for _, binding := range step.Context {
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			value, ok := c.Inputs[parts[1]]
			if !ok {
				return nil, nil, nil, fmt.Errorf("missing input %q", parts[1])
			}
			inputs[binding.As] = value
			continue
		}
		if len(parts) == 3 && parts[0] == "steps" && parts[2] == "output" {
			value, ref, ok, err := c.resolveBindingOutput(ctx, binding, attempts)
			if err != nil {
				return nil, nil, nil, err
			}
			if !ok {
				// Optional-absent binding: resolve to "" with no artifact to
				// reference (the evidence-refs block skips it).
				evidence[binding.As] = ""
				continue
			}
			refs[binding.As] = ref
			evidence[binding.As] = value
			continue
		}
		return nil, nil, nil, fmt.Errorf("unsupported context binding %q", binding.From)
	}
	return inputs, evidence, refs, nil
}

func (c *LinearController) fail(ctx context.Context, run workflowledger.RunSnapshot, cause error) (workflowledger.RunSnapshot, bool, error) {
	return c.failWithStatus(ctx, run, cause, workflowledger.RunStatusFailed)
}

func (c *LinearController) failWithStatus(ctx context.Context, run workflowledger.RunSnapshot, cause error, status workflowledger.RunStatus) (workflowledger.RunSnapshot, bool, error) {
	if !workflowledger.IsTerminalRunStatus(run.Status) {
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

func maxBinding(step definition.Step) int {
	max := 0
	for _, b := range step.Context {
		if b.MaxBytes > max {
			max = b.MaxBytes
		}
	}
	return max
}

// validateBindingLimits measures every context binding's resolved value against
// its max_bytes. Evidence bindings measure the EVIDENCE VALUE already built by
// contextForStep — the inlined value or the reference envelope — never the
// original artifact bytes: an enveloped artifact passes the binding cap, or the
// envelope substitution in contextForStep would be pointless. Inputs bindings
// keep reading the controller's inputs unchanged.
func validateBindingLimits(step definition.Step, inputs map[string]any, evidence map[string]any) error {
	for _, binding := range step.Context {
		if binding.MaxBytes <= 0 {
			continue
		}
		var value any
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 {
			value = inputs[parts[1]]
		} else {
			value = evidence[binding.As]
			if binding.Optional {
				// A missing optional prior output resolves to "" (contextForStep);
				// never reject it against a tiny max_bytes on the first attempt.
				if s, ok := value.(string); ok && s == "" {
					continue
				}
			}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal context binding %q: %w", binding.From, err)
		}
		if len(raw) > binding.MaxBytes {
			return fmt.Errorf("context binding %q exceeds %d bytes", binding.From, binding.MaxBytes)
		}
	}
	return nil
}

func cloneValues(values map[string]any) map[string]any {
	raw, _ := json.Marshal(values)
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out)
	return out
}
