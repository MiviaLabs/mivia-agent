package ledger

import (
	"encoding/hex"
	"encoding/json"
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
	eventKindAttemptCompleted = "wf_attempt_completed"
	eventKindLoopIncremented  = "wf_loop_incremented"
	eventKindApprovalCreated  = "wf_approval_created"
	eventKindApprovalResolved = "wf_approval_resolved"
	eventKindDeliveryUpserted = "wf_delivery_upserted"
)

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
	AttemptID       string        `json:"attempt_id"`
	Status          AttemptStatus `json:"status"`
	OutputRef       string        `json:"output_ref,omitempty"`
	OutputDigest    string        `json:"output_digest,omitempty"`
	ToStepID        string        `json:"to_step_id,omitempty"`
	TransitionIndex int           `json:"transition_index,omitempty"`
	MatchDigest     string        `json:"match_digest,omitempty"`
	DecisionJSON    []byte        `json:"decision_json,omitempty"`
	FinishedAt      time.Time     `json:"finished_at"`
	CreatedAt       time.Time     `json:"created_at"`
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
