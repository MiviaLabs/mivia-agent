package ledger

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Storage event kinds. The wf_ prefix is a HARD namespace rule: the coordinator's
// projection (internal/ledger) ignores unknown kinds, so reusing its kind strings
// (run_created, task_created, ...) would leak workflow runs into its recovery
// surfaces. Every wf event payload is non-empty JSON.
const (
	eventKindRunCreated       = "wf_run_created"
	eventKindRunStatusChanged = "wf_run_status_changed"
	eventKindAttemptStarted   = "wf_attempt_started"
	eventKindAttemptExecution = "wf_attempt_execution"
	eventKindAttemptCompleted = "wf_attempt_completed"
	eventKindPanelPhaseSet    = "wf_panel_phase_set"
	eventKindLoopIncremented  = "wf_loop_incremented"
	eventKindApprovalCreated  = "wf_approval_created"
	eventKindApprovalResolved = "wf_approval_resolved"
	eventKindDeliveryUpserted = "wf_delivery_upserted"
	eventKindRunDeleted       = "wf_run_deleted"
	eventKindRunResumed       = "wf_run_resumed"
)

type panelPhasePayload struct {
	AttemptID string                   `json:"attempt_id"`
	Version   uint64                   `json:"version"`
	Phase     PanelPhase               `json:"phase"`
	Synthesis *PanelSynthesisExecution `json:"synthesis,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
}

func marshalPanelPhase(p panelPhasePayload) ([]byte, error) { return json.Marshal(p) }

func unmarshalPanelPhase(data []byte) (panelPhasePayload, error) {
	var p panelPhasePayload
	err := json.Unmarshal(data, &p)
	return p, err
}

// EventID mints the DETERMINISTIC event ID for a logical key:
//
//	"wfe:" + hex(runID) + ":" + kind + ":" + hex(part) for each dynamic part.
//
// Every dynamic part is hex-encoded so the mapping is injective regardless of
// caller-controlled characters (step IDs, loop names, keys may contain ':').
// The store's events.id PRIMARY KEY is therefore the uniqueness constraint for
// every logical key: a second writer appending the same key gets ErrDuplicate
// (identical payload -> success; different payload -> ErrConflict).
func EventID(runID, kind string, parts ...string) string {
	id := "wfe:" + hex.EncodeToString([]byte(runID)) + ":" + kind
	for _, part := range parts {
		id += ":" + hex.EncodeToString([]byte(part))
	}
	return id
}

// runCreatedPayload is the wf_run_created event payload.
type runCreatedPayload struct {
	Run          RunSnapshot `json:"run"`
	SnapshotJSON []byte      `json:"snapshot_json"`
	CreatedAt    time.Time   `json:"created_at"`
}

// runStatusChangedPayload is the wf_run_status_changed event payload.
type runStatusChangedPayload struct {
	Status     RunStatus  `json:"status"`
	Version    uint64     `json:"version"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// attemptStartedPayload is the wf_attempt_started event payload.
type attemptStartedPayload struct {
	Attempt   StepAttempt `json:"attempt"`
	CreatedAt time.Time   `json:"created_at"`
}

// attemptCompletedPayload is the wf_attempt_completed event payload. ONE event
// carries the terminal status AND the route decision + output evidence; the
// ordered event log is the audit trail (no separate transition/audit event).
type attemptCompletedPayload struct {
	AttemptID        string        `json:"attempt_id"`
	Version          uint64        `json:"version,omitempty"`
	Status           AttemptStatus `json:"status"`
	CoordinatorRunID string        `json:"coordinator_run_id,omitempty"`
	TaskID           string        `json:"task_id,omitempty"`
	OutputRef        string        `json:"output_ref,omitempty"`
	OutputDigest     string        `json:"output_digest,omitempty"`
	ErrorRef         string        `json:"error_ref,omitempty"`
	ToStepID         string        `json:"to_step_id,omitempty"`
	TransitionIndex  int           `json:"transition_index,omitempty"`
	MatchDigest      string        `json:"match_digest,omitempty"`
	DecisionJSON     []byte        `json:"decision_json,omitempty"`
	EvidenceJSON     []byte        `json:"evidence_json,omitempty"`
	FinishedAt       time.Time     `json:"finished_at"`
	CreatedAt        time.Time     `json:"created_at"`
}

// attemptPromptPayload is the wf_attempt_prompt event payload. It carries
// ONLY attempt identity + a content reference — NEVER prompt content: the
// prompt body lives in content-addressed storage and is looked up via
// PromptRef, so the event log stays free of prompt text.
type attemptPromptPayload struct {
	AttemptID string    `json:"attempt_id"`
	PromptRef string    `json:"prompt_ref"`
	CreatedAt time.Time `json:"created_at"`
}

type attemptExecutionPayload struct {
	AttemptID        string    `json:"attempt_id"`
	ExecutionNo      int       `json:"execution_no,omitempty"`
	CoordinatorRunID string    `json:"coordinator_run_id"`
	TaskID           string    `json:"task_id"`
	Reason           string    `json:"reason,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// loopIncrementedPayload is the wf_loop_incremented event payload.
type loopIncrementedPayload struct {
	LoopName   string    `json:"loop_name"`
	Iterations int       `json:"iterations"`
	CreatedAt  time.Time `json:"created_at"`
}

// approvalCreatedPayload is the wf_approval_created event payload.
type approvalCreatedPayload struct {
	Approval  ApprovalRecord `json:"approval"`
	CreatedAt time.Time      `json:"created_at"`
}

// approvalResolvedPayload is the wf_approval_resolved event payload.
type approvalResolvedPayload struct {
	ApprovalID string    `json:"approval_id"`
	Status     string    `json:"status"`
	Actor      string    `json:"actor,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	ResolvedAt time.Time `json:"resolved_at"`
	CreatedAt  time.Time `json:"created_at"`
}

// deliveryUpsertedPayload is the wf_delivery_upserted event payload.
type deliveryUpsertedPayload struct {
	Delivery  DeliveryRecord `json:"delivery"`
	CreatedAt time.Time      `json:"created_at"`
}

// runDeletedPayload is the wf_run_deleted tombstone payload. The store appends
// it in the same transaction that deletes prior events. A second repository
// instance converges to "deleted" because its watermark advances past the
// tombstone and the projection rebuild clears the run. A later incarnation
// starts after the tombstone. Its event IDs and sequences cannot collide with
// the deleted incarnation's IDs and sequences.
type runDeletedPayload struct {
	RunID     string    `json:"run_id"`
	DeletedAt time.Time `json:"deleted_at"`
}

// runResumedPayload is the wf_run_resumed audit event payload: a controller
// re-enters an existing run after a crash, an interrupt, or an operator
// resume. The event is purely observational (the projection ignores it), so
// the payload carries ONLY the run identity. The deterministic event ID is
// (runID, kind) and the payload is byte-identical on every resume, so a
// retried resume appends at most one event under the real clock; the event
// row's append time is the resume instant.
type runResumedPayload struct {
	RunID string `json:"run_id"`
}

// Marshal helpers (json.Marshal of the payload structs).
func marshalRunCreated(p runCreatedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalRunCreated(data []byte) (runCreatedPayload, error) {
	var p runCreatedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalRunStatusChanged(p runStatusChangedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalRunStatusChanged(data []byte) (runStatusChangedPayload, error) {
	var p runStatusChangedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalAttemptStarted(p attemptStartedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalAttemptStarted(data []byte) (attemptStartedPayload, error) {
	var p attemptStartedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalAttemptCompleted(p attemptCompletedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalAttemptCompleted(data []byte) (attemptCompletedPayload, error) {
	var p attemptCompletedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}

// marshalAttemptPrompt encodes the wf_attempt_prompt payload. It rejects
// empty identity/reference fields (the payload must always be resolvable to
// an attempt and its stored prompt) and normalizes a zero CreatedAt to the
// current time so the log never persists the zero timestamp.
func marshalAttemptPrompt(p attemptPromptPayload) ([]byte, error) {
	if p.AttemptID == "" {
		return nil, fmt.Errorf("attempt_prompt payload: attempt_id is empty")
	}
	if p.PromptRef == "" {
		return nil, fmt.Errorf("attempt_prompt payload: prompt_ref is empty")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	return json.Marshal(p)
}
func unmarshalAttemptPrompt(data []byte) (attemptPromptPayload, error) {
	var p attemptPromptPayload
	err := json.Unmarshal(data, &p)
	return p, err
}

func marshalAttemptExecution(p attemptExecutionPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalAttemptExecution(data []byte) (attemptExecutionPayload, error) {
	var p attemptExecutionPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalLoopIncremented(p loopIncrementedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalLoopIncremented(data []byte) (loopIncrementedPayload, error) {
	var p loopIncrementedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalApprovalCreated(p approvalCreatedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalApprovalCreated(data []byte) (approvalCreatedPayload, error) {
	var p approvalCreatedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalApprovalResolved(p approvalResolvedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalApprovalResolved(data []byte) (approvalResolvedPayload, error) {
	var p approvalResolvedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalDeliveryUpserted(p deliveryUpsertedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalDeliveryUpserted(data []byte) (deliveryUpsertedPayload, error) {
	var p deliveryUpsertedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
func marshalRunDeleted(p runDeletedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalRunDeleted(data []byte) (runDeletedPayload, error) {
	var p runDeletedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}

// marshalRunResumed encodes the wf_run_resumed payload. It never stamps the
// clock: the payload must be byte-identical across retried resumes so the
// deterministic event ID dedupes them (idempotent append).
func marshalRunResumed(p runResumedPayload) ([]byte, error) { return json.Marshal(p) }
func unmarshalRunResumed(data []byte) (runResumedPayload, error) {
	var p runResumedPayload
	err := json.Unmarshal(data, &p)
	return p, err
}
