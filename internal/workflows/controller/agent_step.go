package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/template"
)

// AgentStepRunner executes one workflow agent step through the coordinator.
type AgentStepRunner interface {
	RunStep(context.Context, AgentStepRequest) (AgentStepResult, error)
}

// StepRunJoiner is an optional AgentStepRunner capability: join a previously
// dispatched step's coordinator run by its recorded identity and report its
// terminal outcome. The ledger contract (internal/workflows/ledger/recovery.go)
// requires a recorded in-flight attempt to be JOINED, never re-dispatched, so
// the controller tries JoinStep on resume before admitting a fresh attempt.
// A runner that cannot join (test seams, non-coordinator runners) simply does
// not implement this interface; the controller then falls back to interrupting
// the stale attempt and admitting a fresh one.
type StepRunJoiner interface {
	// JoinStep joins the coordinator run named by spec.CoordinatorRunID and
	// waits for its terminal outcome. joined=true means the child ran (or is
	// being joined to completion) and result carries its terminal status
	// ("completed", "failed", "timed_out", "canceled"); the caller must
	// complete the attempt with that outcome instead of re-dispatching.
	// joined=false means there was nothing to join (the child never ran, the
	// run is unknown, or the join could not be completed) and the caller may
	// interrupt the stale attempt and re-dispatch fresh.
	JoinStep(context.Context, AgentStepRequest) (AgentStepResult, bool, error)
}

// RouteDecision is the durable transition decision attached to one attempt.
type RouteDecision struct {
	ToStepID        string
	TransitionIndex int
	MatchDigest     string
	DecisionJSON    []byte
	// Loop is set when the selected transition is a named back-edge.
	// The controller checks the loop cap before route selection returns, and
	// increments the counter only after the attempt completion is durable.
	Loop          string
	MaxIterations int
}

// RecordStepResult writes the child identity and bounded evidence selection to
// one workflow attempt. The controller calls it after attempt admission.
func RecordStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus) error {
	return recordStepResult(ctx, repo, attempt, result, status, RouteDecision{})
}

func recordStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus, route RouteDecision) error {
	if repo == nil {
		return fmt.Errorf("workflow ledger is nil")
	}
	ctx = workflowledger.ContextWithRunID(ctx, attempt.RunID)
	if len(result.EvidenceJSON) > workflowledger.MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds %d bytes", workflowledger.MaxEvidenceBytes)
	}
	attempt.CoordinatorRunID = result.CoordinatorRunID
	attempt.TaskID = result.TaskID
	var outputRef string
	if len(result.Output) > 0 && status == workflowledger.AttemptStatusSucceeded {
		outputRef = "sha256:" + workflowledger.DigestHex(result.Output)
		if err := repo.StoreContent(ctx, outputRef, result.Output); err != nil {
			return err
		}
	}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return err
	}
	outcome := workflowledger.AttemptOutcome{
		Status: status, CoordinatorRunID: result.CoordinatorRunID, TaskID: result.TaskID,
		EvidenceJSON: append([]byte(nil), result.EvidenceJSON...), OutputRef: outputRef,
		ErrorRef: result.ErrorRef,
		ToStepID: route.ToStepID, TransitionIndex: route.TransitionIndex,
		MatchDigest: route.MatchDigest, DecisionJSON: append([]byte(nil), route.DecisionJSON...),
	}
	if outputRef != "" {
		outcome.OutputDigest = workflowledger.DigestHex(result.Output)
	}
	return repo.CompleteStepAttempt(ctx, attempt.RunID, attempt.AttemptID, 1, outcome)
}

// CompleteExistingStepResult completes an attempt that the controller already
// recorded before an interruption. The stable child key prevents re-dispatch.
func CompleteExistingStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus, route RouteDecision) error {
	if repo == nil {
		return fmt.Errorf("workflow ledger is nil")
	}
	ctx = workflowledger.ContextWithRunID(ctx, attempt.RunID)
	if len(result.EvidenceJSON) > workflowledger.MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds %d bytes", workflowledger.MaxEvidenceBytes)
	}
	outcome := workflowledger.AttemptOutcome{
		Status: status, CoordinatorRunID: result.CoordinatorRunID, TaskID: result.TaskID,
		EvidenceJSON: append([]byte(nil), result.EvidenceJSON...),
		ErrorRef:     result.ErrorRef,
		ToStepID:     route.ToStepID, TransitionIndex: route.TransitionIndex,
		MatchDigest: route.MatchDigest, DecisionJSON: append([]byte(nil), route.DecisionJSON...),
	}
	// Output is stored for every status: evidence-gate repair steps bind the
	// failed gate's verification output (pinned by phase4 tests), and a
	// succeeded child whose step errored keeps its output (D3). Agent steps
	// that genuinely fail route to on_failure, never repair, so partial agent
	// output is not injected into repair context.
	if len(result.Output) > 0 {
		outcome.OutputDigest = workflowledger.DigestHex(result.Output)
		outcome.OutputRef = "sha256:" + outcome.OutputDigest
		if err := repo.StoreContent(ctx, outcome.OutputRef, result.Output); err != nil {
			return err
		}
	}
	return repo.CompleteStepAttempt(ctx, attempt.RunID, attempt.AttemptID, attempt.Version, outcome)
}

// maxStepContextBytes bounds the rendered agent prompt for one workflow step.
// It is deliberately larger than one evidence binding so a step that binds
// several legal values renders successfully: the template file itself is
// bounded by MaxTemplateBytes, and the routed model budget (~72k tokens) plus
// the schema-aware prune guard the actual request. The aggregate evidence
// selection metadata is bounded separately by workflowledger.MaxEvidenceBytes
// inside marshalEvidenceSelection, before the child is dispatched.
const maxStepContextBytes = 256 << 10

// AgentStepRequest contains only the explicit, bounded step inputs.
type AgentStepRequest struct {
	WorkflowRunID    string
	StepID           string
	AttemptNo        int
	TaskID           string
	CoordinatorRunID string
	AgentName        string
	AgentDigest      string
	Skill            string
	ProviderName     string
	Model            string
	Scope            string
	Permission       string
	Timeout          time.Duration
	Budget           int
	ForceResume      bool
	Template         string
	Inputs           map[string]any
	Evidence         map[string]any
	MaxBindingBytes  int
	MaxContextBytes  int
	OutputSchema     map[string]any
	// Prompt is the fully rendered step prompt (including the evidence-refs
	// block), produced by the controller. An empty value means the runner
	// must render the prompt from Template/Inputs/Evidence.
	Prompt string
	// EvidenceRefs names the artifact references bound into the prompt,
	// keyed by evidence name. Nil when the step binds no artifact
	// references.
	EvidenceRefs map[string]ArtifactRef
}

// ArtifactRef addresses one content-addressed artifact referenced by a
// workflow step's evidence.
type ArtifactRef struct {
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
	Ref     string `json:"ref"`
	Bytes   int    `json:"bytes"`
	Digest  string `json:"digest"`
}

// AgentStepResult contains the validated output and bounded evidence metadata.
type AgentStepResult struct {
	CoordinatorRunID string
	TaskID           string
	Output           json.RawMessage
	ValidatedOutput  any
	EvidenceJSON     []byte
	// Status is the child task's terminal status from the coordinator result
	// ("completed", "failed", "timed_out", "canceled", "blocked"). It is
	// empty when the step runner produced no child result (for example a
	// pre-dispatch failure).
	Status string
	// ErrorRef names content-addressed failure detail for a failed attempt.
	// It is set by the controller from the step error; an empty value means
	// no error detail was persisted.
	ErrorRef string
}

// SchemaValidationError marks output that fails the declared step schema.
type SchemaValidationError struct {
	StepID string
	Err    error
}

func (e *SchemaValidationError) Error() string {
	if e == nil || e.Err == nil {
		return "workflow step output schema validation failed"
	}
	return fmt.Sprintf("workflow step %q output schema validation failed", e.StepID)
}

func (e *SchemaValidationError) Unwrap() error { return e.Err }

// CoordinatorRunner is the production implementation of AgentStepRunner.
type CoordinatorRunner struct {
	Coordinator coordinator.Coordinator
	// JoinWatchdog bounds a coordinator join from the controller side. The
	// coordinator's own Join (internal/coordinator/coordinator.go) waits on
	// the child run's done channel with no bound of its own, so a child that
	// never settles (hung pool worker, stuck referral wait, dead executor)
	// would park the controller forever. A value <= 0 uses
	// defaultJoinWatchdog; tests set it short to exercise the join-timeout
	// path.
	JoinWatchdog time.Duration
	// progressEmitter is an optional sink for periodic step-heartbeat
	// progress events emitted while a join is live. It is nil-safe and must
	// be concurrency-safe: the join loop calls it from its own goroutine.
	progressEmitter func(ProgressEvent)
}

// SetProgressEmitter wires an optional step-heartbeat emitter into the
// runner. The emitter receives a ProgressStepHeartbeat per watchdog tick
// while a join is live. Production wiring connects it to the controller's
// progress sink (see newWorkflowController); tests may leave it nil.
func (r *CoordinatorRunner) SetProgressEmitter(emitter func(ProgressEvent)) {
	r.progressEmitter = emitter
}

var _ AgentStepRunner = (*CoordinatorRunner)(nil)

// NewCoordinatorRunner creates a workflow step adapter.
func NewCoordinatorRunner(c coordinator.Coordinator) *CoordinatorRunner {
	return &CoordinatorRunner{Coordinator: c}
}

// RunStep renders the bounded prompt, dispatches one child task, and validates
// the child's final JSON output. The child idempotency key is run-scoped.
func (r *CoordinatorRunner) RunStep(ctx context.Context, spec AgentStepRequest) (AgentStepResult, error) {
	if r == nil || r.Coordinator == nil {
		return AgentStepResult{}, fmt.Errorf("agent step runner has no coordinator")
	}
	if err := validateRequest(spec); err != nil {
		return AgentStepResult{}, err
	}
	var prompt string
	if spec.Prompt != "" {
		prompt = spec.Prompt
	} else {
		var err error
		prompt, err = template.Render(spec.Template, spec.Inputs, spec.Evidence, spec.MaxBindingBytes, spec.MaxContextBytes)
		if err != nil {
			return AgentStepResult{}, err
		}
	}
	evidenceJSON, err := marshalEvidenceSelection(spec)
	if err != nil {
		return AgentStepResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AgentStepResult{}, err
	}
	task := workflowTask(ctx, spec, prompt)
	h, err := r.dispatch(ctx, spec, task)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: spec.CoordinatorRunID, TaskID: spec.TaskID, EvidenceJSON: evidenceJSON}, err
	}
	return r.finish(ctx, spec, h, evidenceJSON)
}

// JoinStep implements StepRunJoiner for the production coordinator runner.
// It re-dispatches the recorded CoordinatorRunID/TaskID through the same
// dispatch+join machinery as RunStep: coordinator.EnsureRun is idempotent on
// the workflow step's identity key, so an EXISTING child run is resumed and
// joined (a completed child yields its recorded outcome without re-executing
// its work) instead of creating a fresh run. The child's terminal status is
// reported in result.Status; joined=false when no child outcome is available
// (the run is unknown, an idempotency conflict from changed step inputs or a
// drifted deadline-derived timeout, or a join-boundary error), in which case
// the controller interrupts the stale attempt and re-dispatches fresh.
func (r *CoordinatorRunner) JoinStep(ctx context.Context, spec AgentStepRequest) (AgentStepResult, bool, error) {
	result, err := r.RunStep(ctx, spec)
	return result, result.Status != "", err
}

func workflowTask(ctx context.Context, spec AgentStepRequest, prompt string) subagents.Task {
	task := subagents.Task{ID: spec.TaskID, Name: spec.AgentName, AgentName: spec.AgentName,
		AgentDigest: spec.AgentDigest, Skill: spec.Skill, ProviderName: spec.ProviderName,
		Model: spec.Model, Scope: spec.Scope, Permission: spec.Permission, Input: mustJSON(prompt),
		Timeout: spec.Timeout, Budget: spec.Budget, OutputSchema: cloneSchema(spec.OutputSchema)}
	if caller, ok := runtime.CallerFrom(ctx); ok {
		task.SessionID, task.TurnID, task.Role = caller.SessionID, caller.TurnID, caller.Role
	}
	return task
}

func (r *CoordinatorRunner) dispatch(ctx context.Context, spec AgentStepRequest, task subagents.Task) (*coordinator.RunHandle, error) {
	detached := context.Background()
	if caller, ok := runtime.CallerFrom(ctx); ok {
		detached = runtime.ContextWithCaller(detached, caller)
	}
	h, err := r.Coordinator.EnsureRun(detached, coordinator.EnsureRunRequest{
		RunID: spec.CoordinatorRunID, Tasks: []subagents.Task{task},
		IdempotencyKey: idempotencyKey(spec), ForceResume: spec.ForceResume,
		// The workflow controller is a non-interactive parent: it runs steps
		// unattended and can never answer child questions, so child parks must
		// be auto-declined at park time instead of burning wait_seconds.
		NonInteractiveParent: true,
	})
	if err != nil {
		return nil, err
	}
	if h.RunID() != spec.CoordinatorRunID {
		_ = r.Coordinator.Cancel(context.Background(), h)
		return nil, fmt.Errorf("coordinator returned run %q, want %q", h.RunID(), spec.CoordinatorRunID)
	}
	return h, nil
}

func (r *CoordinatorRunner) finish(ctx context.Context, spec AgentStepRequest, h *coordinator.RunHandle, evidenceJSON []byte) (AgentStepResult, error) {
	run, err := r.Coordinator.Inspect(context.Background(), h)
	if err != nil {
		_ = r.Coordinator.Cancel(context.Background(), h)
		return AgentStepResult{CoordinatorRunID: h.RunID(), TaskID: spec.TaskID, EvidenceJSON: evidenceJSON}, err
	}
	if run.RunID != spec.CoordinatorRunID {
		_ = r.Coordinator.Cancel(context.Background(), h)
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: spec.TaskID, EvidenceJSON: evidenceJSON}, fmt.Errorf("coordinator inspected run %q, want %q", run.RunID, spec.CoordinatorRunID)
	}
	if len(run.Tasks) != 1 || run.Tasks[0].TaskID != spec.TaskID {
		_ = r.Coordinator.Cancel(context.Background(), h)
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: spec.TaskID, EvidenceJSON: evidenceJSON}, fmt.Errorf("coordinator task identity does not match %q", spec.TaskID)
	}
	actualTaskID := spec.TaskID
	joined, err := r.joinWithCancellation(ctx, spec, h, r.progressEmitter)
	if err != nil {
		// A canceled or expired wait still carries the child outcome when the
		// coordinator settled: keep the result so the attempt records output
		// and status instead of a bare failure.
		result := AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}
		if joined != nil {
			if child, findErr := findResult(joined.Results, actualTaskID); findErr == nil {
				applyChildResult(&result, child)
			}
		}
		return result, err
	}
	if joined == nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, fmt.Errorf("coordinator returned no result")
	}
	result, err := findResult(joined.Results, actualTaskID)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, err
	}
	if result.Err != nil {
		out := AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}
		applyChildResult(&out, result)
		return out, result.Err
	}
	if joined.Err != nil {
		// The child succeeded but run-level persistence failed. Keep the
		// child's output and status so the attempt records them together
		// with the step error (D3: no silent output loss).
		out := AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}
		applyChildResult(&out, result)
		return out, joined.Err
	}
	output := extractTaskOutput(result.Output)
	validated, err := validateOutput(spec.StepID, output, spec.OutputSchema)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, Output: output, EvidenceJSON: evidenceJSON, Status: result.Status}, err
	}
	return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, Output: output, ValidatedOutput: validated, EvidenceJSON: evidenceJSON, Status: result.Status}, nil
}

// applyChildResult copies the child task's terminal status and output onto a
// step result. Output is extracted from the result envelope like the success
// path does.
func applyChildResult(out *AgentStepResult, res subagents.Result) {
	out.Status = res.Status
	if len(res.Output) > 0 {
		out.Output = extractTaskOutput(res.Output)
	}
}

func extractTaskOutput(raw json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	_, hasStatus := envelope["status"]
	_, hasSchema := envelope["schema"]
	if (hasStatus || hasSchema) && envelope["output"] != nil {
		return append(json.RawMessage(nil), envelope["output"]...)
	}
	return append(json.RawMessage(nil), raw...)
}

func validateRequest(spec AgentStepRequest) error {
	if spec.WorkflowRunID == "" || spec.StepID == "" || spec.TaskID == "" || spec.CoordinatorRunID == "" || spec.AgentName == "" {
		return fmt.Errorf("workflow agent step identity is incomplete")
	}
	if spec.AttemptNo <= 0 {
		return fmt.Errorf("workflow agent step attempt must be positive")
	}
	if spec.Budget < 0 || spec.Timeout < 0 {
		return fmt.Errorf("workflow agent step limits must be non-negative")
	}
	return nil
}

func idempotencyKey(spec AgentStepRequest) string {
	return "workflow-step/" + spec.WorkflowRunID + "/" + spec.StepID + "/" + fmt.Sprint(spec.AttemptNo) + "/" + spec.TaskID
}

func mustJSON(value string) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func cloneSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	raw, _ := json.Marshal(schema)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func validateOutput(stepID string, raw json.RawMessage, schema map[string]any) (any, error) {
	if len(raw) == 0 {
		return nil, &SchemaValidationError{StepID: stepID, Err: jschema.ErrValidation}
	}
	if schema == nil {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, &SchemaValidationError{StepID: stepID, Err: fmt.Errorf("%w: invalid JSON", jschema.ErrValidation)}
		}
		return value, nil
	}
	compiled, err := jschema.Compile(schema)
	if err != nil {
		return nil, fmt.Errorf("compile step output schema: %w", err)
	}
	value, err := compiled.ValidateJSONBytes(raw)
	if err != nil {
		return nil, &SchemaValidationError{StepID: stepID, Err: err}
	}
	return value, nil
}

func findResult(results []subagents.Result, taskID string) (subagents.Result, error) {
	for _, result := range results {
		if result.TaskID == taskID {
			return result, nil
		}
	}
	return subagents.Result{}, fmt.Errorf("coordinator result for task %q is missing", taskID)
}
