package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// StepRuntime contains snapshotted data required to execute one agent step.
type StepRuntime struct {
	Agent    agents.ResolvedAgent
	Digest   string
	Template string
	Schema   map[string]any
}

// LinearController advances a workflow with one agent step at a time.
// It deliberately rejects gates, loops, and ambiguous routes in Phase 3.
type LinearController struct {
	Repo     workflowledger.Repository
	Runner   AgentStepRunner
	Workflow *compiler.CompiledWorkflow
	Steps    map[string]StepRuntime
	Inputs   map[string]any
	RunID    string
	Snapshot []byte
	Holder   string
	mu       sync.Mutex
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
	for _, transition := range wf.Transitions {
		if transition.Loop != "" {
			return nil, fmt.Errorf("phase 3 does not support loop transition %q", transition.Loop)
		}
	}
	if hasCycle(wf) {
		return nil, fmt.Errorf("phase 3 does not support cyclic workflow transitions")
	}
	return &LinearController{Repo: repo, Runner: runner, Workflow: wf, Steps: steps, Inputs: cloneValues(inputs), RunID: runID, Snapshot: append([]byte(nil), snapshot...), Holder: newWorkflowHolder()}, nil
}

// Start admits the run. It is idempotent for the same run ID and snapshot.
func (c *LinearController) Start(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("linear controller is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := workflowledger.RunSnapshot{RunID: c.RunID, WorkflowName: c.Workflow.Name, WorkflowDigest: c.Workflow.Digest, Status: workflowledger.RunStatusPending, ActiveStepID: c.Workflow.InitialStep}
	if err := c.Repo.CreateRun(ctx, snap, c.Snapshot); err != nil {
		if !errors.Is(err, workflowledger.ErrDuplicate) {
			return err
		}
		stored, getErr := c.Repo.GetRunSnapshot(ctx, c.RunID)
		if getErr != nil {
			return getErr
		}
		if !bytes.Equal(stored, c.Snapshot) {
			return fmt.Errorf("workflow run %q already exists with a different snapshot", c.RunID)
		}
	}
	return nil
}

// Run advances until the run reaches a terminal status.
func (c *LinearController) Run(ctx context.Context) (workflowledger.RunSnapshot, error) {
	if c.Workflow.Limits.MaxDurationSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(c.Workflow.Limits.MaxDurationSeconds)*time.Second)
		defer cancel()
	}
	if err := c.Start(ctx); err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	for {
		snap, done, err := c.Advance(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				current, getErr := c.Repo.GetRun(context.Background(), c.RunID)
				if getErr == nil && !workflowledger.IsTerminalRunStatus(current.Status) {
					_, _, _ = c.failWithStatus(context.Background(), current, context.DeadlineExceeded, workflowledger.RunStatusTimedOut)
				}
			}
			return snap, err
		}
		if done {
			return snap, nil
		}
	}
}

// Advance executes the current step once. It returns done when the run is terminal.
func (c *LinearController) Advance(ctx context.Context) (workflowledger.RunSnapshot, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.Repo.ClaimRun(ctx, c.RunID, c.Holder); err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	defer func() { _ = c.Repo.ReleaseRun(context.Background(), c.RunID, c.Holder) }()
	run, err := c.Repo.GetRun(ctx, c.RunID)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
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
	step, ok := c.WorkflowStep(run.ActiveStepID)
	if !ok {
		return c.fail(ctx, run, fmt.Errorf("workflow step %q is not declared", run.ActiveStepID))
	}
	if step.Kind != "agent" {
		return c.fail(ctx, run, fmt.Errorf("phase 3 supports agent steps only; step %q is %q", step.ID, step.Kind))
	}
	runtime, ok := c.Steps[step.ID]
	if !ok {
		return c.fail(ctx, run, fmt.Errorf("step %q has no snapshotted runtime", step.ID))
	}
	return c.advanceAgentStep(ctx, run, step, runtime)
}

func (c *LinearController) advanceAgentStep(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime) (workflowledger.RunSnapshot, bool, error) {
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return run, false, err
	}
	attempt, existing := latestAttempt(attempts, step.ID)
	if !existing {
		attempt = workflowledger.StepAttempt{AttemptID: fmt.Sprintf("wfa-%s-%d", step.ID, nextAttemptNo(attempts, step.ID)), RunID: c.RunID, StepID: step.ID, AttemptNo: nextAttemptNo(attempts, step.ID)}
		attempt.Status = workflowledger.AttemptStatusRunning
		if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
			return c.fail(ctx, run, err)
		}
		attempt, err = c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
		if err != nil {
			return c.fail(ctx, run, err)
		}
		existing = true
	}
	if workflowledger.IsTerminalAttemptStatus(attempt.Status) {
		if c.Workflow.Limits.MaxStepAttempts > 0 && attempt.AttemptNo >= c.Workflow.Limits.MaxStepAttempts {
			return c.fail(ctx, run, fmt.Errorf("step %q exceeded max attempts", step.ID))
		}
		attempt = workflowledger.StepAttempt{AttemptID: fmt.Sprintf("wfa-%s-%d", step.ID, attempt.AttemptNo+1), RunID: c.RunID, StepID: step.ID, AttemptNo: attempt.AttemptNo + 1, Status: workflowledger.AttemptStatusRunning}
		if err := c.Repo.CreateStepAttempt(ctx, attempt); err != nil {
			return c.fail(ctx, run, err)
		}
		attempt, err = c.Repo.GetStepAttempt(ctx, c.RunID, attempt.AttemptID)
		if err != nil {
			return c.fail(ctx, run, err)
		}
	}
	return c.executeAgentAttempt(ctx, run, step, runtime, attempt, attempts, existing)
}

func (c *LinearController) executeAgentAttempt(ctx context.Context, run workflowledger.RunSnapshot, step definition.Step, runtime StepRuntime, attempt workflowledger.StepAttempt, attempts []workflowledger.StepAttempt, existing bool) (workflowledger.RunSnapshot, bool, error) {
	stepInputs, evidence, err := c.contextForStep(ctx, step, attempts)
	if err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	if err := validateBindingLimits(step, c.Inputs, attempts, c.Repo, ctx); err != nil {
		return c.failAttempt(ctx, run, attempt, err)
	}
	taskID := attempt.TaskID
	if taskID == "" {
		taskID = fmt.Sprintf("wft-%s-%d", step.ID, attempt.AttemptNo)
	}
	var timeout time.Duration
	if runtime.Agent.TimeoutSeconds != nil {
		timeout = time.Duration(*runtime.Agent.TimeoutSeconds) * time.Second
	}
	budget := 0
	if runtime.Agent.MaxTokens != nil {
		budget = *runtime.Agent.MaxTokens
	}
	req := AgentStepRequest{WorkflowRunID: c.RunID, StepID: step.ID, AttemptNo: attempt.AttemptNo, TaskID: taskID, CoordinatorRunID: attempt.CoordinatorRunID, AgentName: runtime.Agent.Name, AgentDigest: runtime.Digest, ProviderName: runtime.Agent.Provider, Model: runtime.Agent.Model, Timeout: timeout, Budget: budget, Template: runtime.Template, Inputs: stepInputs, Evidence: evidence, MaxBindingBytes: maxBinding(step), MaxContextBytes: 32 << 10, OutputSchema: runtime.Schema}
	result, runErr := c.Runner.RunStep(ctx, req)
	status := workflowledger.AttemptStatusSucceeded
	if runErr != nil {
		status = workflowledger.AttemptStatusFailed
		if errors.Is(runErr, context.Canceled) {
			status = workflowledger.AttemptStatusCanceled
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			status = workflowledger.AttemptStatusTimedOut
		}
	}
	next := ""
	if runErr == nil {
		var routeErr error
		next, routeErr = c.nextStep(step, result.ValidatedOutput, nil)
		if routeErr != nil {
			status = workflowledger.AttemptStatusFailed
			runErr = routeErr
		}
	}
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	if existing {
		err = CompleteExistingStepResult(writeCtx, c.Repo, attempt, result, status, next)
	} else {
		err = recordStepResult(writeCtx, c.Repo, attempt, result, status, next)
	}
	if err != nil {
		return c.fail(writeCtx, run, err)
	}
	if runErr != nil {
		runStatus := workflowledger.RunStatusFailed
		if status == workflowledger.AttemptStatusCanceled {
			runStatus = workflowledger.RunStatusCanceled
		} else if status == workflowledger.AttemptStatusTimedOut {
			runStatus = workflowledger.RunStatusTimedOut
		}
		return c.failWithStatus(writeCtx, run, runErr, runStatus)
	}
	if next == "success" {
		run, err = c.Repo.GetRun(ctx, c.RunID)
		if err != nil {
			return run, false, err
		}
		if err := c.Repo.CompareAndSetRunStatus(ctx, c.RunID, run.Version, workflowledger.RunStatusSucceeded, nil); err != nil {
			return run, false, err
		}
		run, err = c.Repo.GetRun(ctx, c.RunID)
		return run, true, err
	}
	run, err = c.Repo.GetRun(ctx, c.RunID)
	return run, false, err
}

func stepPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (c *LinearController) failAttempt(ctx context.Context, run workflowledger.RunSnapshot, attempt workflowledger.StepAttempt, cause error) (workflowledger.RunSnapshot, bool, error) {
	writeCtx, cancel := stepPersistenceContext(ctx)
	defer cancel()
	_ = CompleteExistingStepResult(writeCtx, c.Repo, attempt, AgentStepResult{}, workflowledger.AttemptStatusFailed, "")
	return c.fail(writeCtx, run, cause)
}

func (c *LinearController) WorkflowStep(id string) (definition.Step, bool) {
	for _, step := range c.Workflow.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return definition.Step{}, false
}

func (c *LinearController) contextForStep(ctx context.Context, step definition.Step, attempts []workflowledger.StepAttempt) (map[string]any, map[string]any, error) {
	inputs := make(map[string]any)
	evidence := make(map[string]any)
	for _, binding := range step.Context {
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			value, ok := c.Inputs[parts[1]]
			if !ok {
				return nil, nil, fmt.Errorf("missing input %q", parts[1])
			}
			inputs[binding.As] = value
			continue
		}
		if len(parts) == 3 && parts[0] == "steps" && parts[2] == "output" {
			prior, ok := latestAttempt(attempts, parts[1])
			if !ok || prior.OutputRef == "" {
				return nil, nil, fmt.Errorf("missing prior output %q", binding.From)
			}
			raw, err := c.Repo.LoadContent(ctx, prior.OutputRef)
			if err != nil {
				return nil, nil, err
			}
			if len(raw) > 32<<10 {
				return nil, nil, fmt.Errorf("prior output %q exceeds 32768 bytes", binding.From)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, nil, fmt.Errorf("decode prior output: %w", err)
			}
			evidence[binding.As] = value
			continue
		}
		return nil, nil, fmt.Errorf("unsupported context binding %q", binding.From)
	}
	return inputs, evidence, nil
}

func (c *LinearController) nextStep(step definition.Step, output any, runErr error) (string, error) {
	if runErr != nil {
		return "failure", nil
	}
	var selected string
	for _, transition := range c.Workflow.Transitions {
		if transition.From != step.ID || transition.Match.Status != "succeeded" {
			continue
		}
		if len(transition.Match.Output) > 0 {
			object, ok := output.(map[string]any)
			if !ok {
				continue
			}
			matched := true
			for key, want := range transition.Match.Output {
				if fmt.Sprint(object[key]) != want {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		if selected != "" {
			return "failure", fmt.Errorf("step %q has multiple matching transitions", step.ID)
		}
		selected = transition.To
	}
	if selected == "" {
		return "failure", fmt.Errorf("step %q has no matching transition", step.ID)
	}
	return selected, nil
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

func latestAttempt(attempts []workflowledger.StepAttempt, step string) (workflowledger.StepAttempt, bool) {
	var latest workflowledger.StepAttempt
	found := false
	for _, attempt := range attempts {
		if attempt.StepID == step && (!found || attempt.AttemptNo > latest.AttemptNo) {
			latest, found = attempt, true
		}
	}
	return latest, found
}

func nextAttemptNo(attempts []workflowledger.StepAttempt, step string) int {
	latest, ok := latestAttempt(attempts, step)
	if !ok {
		return 1
	}
	return latest.AttemptNo + 1
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

func validateBindingLimits(step definition.Step, inputs map[string]any, attempts []workflowledger.StepAttempt, repo workflowledger.Repository, ctx context.Context) error {
	for _, binding := range step.Context {
		if binding.MaxBytes <= 0 {
			continue
		}
		var value any
		parts := strings.Split(binding.From, ".")
		if len(parts) == 2 {
			value = inputs[parts[1]]
		} else {
			prior, ok := latestAttempt(attempts, parts[1])
			if !ok || prior.OutputRef == "" {
				return fmt.Errorf("missing prior output %q", binding.From)
			}
			raw, err := repo.LoadContent(ctx, prior.OutputRef)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("decode prior output: %w", err)
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

func hasCycle(wf *compiler.CompiledWorkflow) bool {
	graph := make(map[string][]string)
	for _, transition := range wf.Transitions {
		if wf.StepIDs[transition.From] && wf.StepIDs[transition.To] {
			graph[transition.From] = append(graph[transition.From], transition.To)
		}
	}
	state := make(map[string]uint8)
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, next := range graph[id] {
			if visit(next) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range graph {
		if visit(id) {
			return true
		}
	}
	return false
}

func cloneValues(values map[string]any) map[string]any {
	raw, _ := json.Marshal(values)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func newWorkflowRunID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "wfr-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
func newWorkflowHolder() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "controller-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
