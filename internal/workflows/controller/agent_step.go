package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

// RecordStepResult writes the child identity and bounded evidence selection to
// one workflow attempt. The controller calls it after attempt admission.
func RecordStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus) error {
	return recordStepResult(ctx, repo, attempt, result, status, "")
}

func recordStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus, toStep string) error {
	if repo == nil {
		return fmt.Errorf("workflow ledger is nil")
	}
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
	outcome := workflowledger.AttemptOutcome{Status: status, CoordinatorRunID: result.CoordinatorRunID, TaskID: result.TaskID, EvidenceJSON: append([]byte(nil), result.EvidenceJSON...), ToStepID: toStep, OutputRef: outputRef}
	if outputRef != "" {
		outcome.OutputDigest = workflowledger.DigestHex(result.Output)
	}
	return repo.CompleteStepAttempt(ctx, attempt.RunID, attempt.AttemptID, 1, outcome)
}

// CompleteExistingStepResult completes an attempt that the controller already
// recorded before an interruption. The stable child key prevents re-dispatch.
func CompleteExistingStepResult(ctx context.Context, repo workflowledger.Repository, attempt workflowledger.StepAttempt, result AgentStepResult, status workflowledger.AttemptStatus, toStep string) error {
	if repo == nil {
		return fmt.Errorf("workflow ledger is nil")
	}
	if len(result.EvidenceJSON) > workflowledger.MaxEvidenceBytes {
		return fmt.Errorf("evidence exceeds %d bytes", workflowledger.MaxEvidenceBytes)
	}
	outcome := workflowledger.AttemptOutcome{Status: status, CoordinatorRunID: result.CoordinatorRunID, TaskID: result.TaskID, EvidenceJSON: append([]byte(nil), result.EvidenceJSON...), ToStepID: toStep}
	if len(result.Output) > 0 && status == workflowledger.AttemptStatusSucceeded {
		outcome.OutputDigest = workflowledger.DigestHex(result.Output)
		outcome.OutputRef = "sha256:" + outcome.OutputDigest
		if err := repo.StoreContent(ctx, outcome.OutputRef, result.Output); err != nil {
			return err
		}
	}
	return repo.CompleteStepAttempt(ctx, attempt.RunID, attempt.AttemptID, attempt.Version, outcome)
}

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
}

// AgentStepResult contains the validated output and bounded evidence metadata.
type AgentStepResult struct {
	CoordinatorRunID string
	TaskID           string
	Output           json.RawMessage
	ValidatedOutput  any
	EvidenceJSON     []byte
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
	prompt, err := template.Render(spec.Template, spec.Inputs, spec.Evidence, spec.MaxBindingBytes, spec.MaxContextBytes)
	if err != nil {
		return AgentStepResult{}, err
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
	joined, err := r.joinWithCancellation(ctx, h)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, err
	}
	if joined == nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, fmt.Errorf("coordinator returned no result")
	}
	result, err := findResult(joined.Results, actualTaskID)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, err
	}
	if result.Err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, result.Err
	}
	if joined.Err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, EvidenceJSON: evidenceJSON}, joined.Err
	}
	output := extractTaskOutput(result.Output)
	validated, err := validateOutput(spec.StepID, output, spec.OutputSchema)
	if err != nil {
		return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, Output: output, EvidenceJSON: evidenceJSON}, err
	}
	return AgentStepResult{CoordinatorRunID: run.RunID, TaskID: actualTaskID, Output: output, ValidatedOutput: validated, EvidenceJSON: evidenceJSON}, nil
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

func (r *CoordinatorRunner) joinWithCancellation(ctx context.Context, h *coordinator.RunHandle) (*coordinator.RunResult, error) {
	result, err := r.Coordinator.Join(ctx, h)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return result, err
	}
	_ = r.Coordinator.Cancel(context.Background(), h)
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = r.Coordinator.Join(cleanup, h)
	return nil, err
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

type evidenceSelection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Bytes  int    `json:"bytes"`
	Digest string `json:"digest"`
}

func marshalEvidenceSelection(spec AgentStepRequest) ([]byte, error) {
	items := make([]evidenceSelection, 0, len(spec.Inputs)+len(spec.Evidence))
	appendItems := func(source string, values map[string]any) error {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			raw, err := json.Marshal(values[key])
			if err != nil {
				return fmt.Errorf("marshal %s binding %q: %w", source, key, err)
			}
			if spec.MaxBindingBytes > 0 && len(raw) > spec.MaxBindingBytes {
				return fmt.Errorf("%s binding %q exceeds %d bytes", source, key, spec.MaxBindingBytes)
			}
			sum := sha256.Sum256(raw)
			items = append(items, evidenceSelection{Name: key, Source: source, Bytes: len(raw), Digest: "sha256:" + hex.EncodeToString(sum[:])})
		}
		return nil
	}
	if err := appendItems("input", spec.Inputs); err != nil {
		return nil, err
	}
	if err := appendItems("evidence", spec.Evidence); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("marshal evidence selection: %w", err)
	}
	if spec.MaxContextBytes > 0 && len(raw) > spec.MaxContextBytes {
		return nil, fmt.Errorf("evidence selection exceeds %d bytes", spec.MaxContextBytes)
	}
	return raw, nil
}

func findResult(results []subagents.Result, taskID string) (subagents.Result, error) {
	for _, result := range results {
		if result.TaskID == taskID {
			return result, nil
		}
	}
	return subagents.Result{}, fmt.Errorf("coordinator result for task %q is missing", taskID)
}
