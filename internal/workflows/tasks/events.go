package tasks

import (
	"encoding/hex"
	"time"
)

// Event kinds and namespaces. The tksp: run prefix and tke: event ID prefix
// are HARD namespace rules: they never collide with the workflow ledger
// (wfr- runs, wfe: IDs, wf_* kinds) or the coordinator (run- runs, se- IDs),
// so both projections treat task events as foreign and ignore them.
const (
	eventKindPlanStored       = "tks_plan_stored"
	eventKindPlanBound        = "tks_plan_bound"
	eventKindTaskCreated      = "tks_task_created"
	eventKindTaskTransitioned = "tks_task_transitioned"

	// runIDPrefix names the store run that holds one plan's event log.
	runIDPrefix = "tksp:"
)

// planRunID names the store run that holds one plan's event log.
func planRunID(planRef string) string { return runIDPrefix + planRef }

// eventID mints the DETERMINISTIC event ID for a logical key:
//
//	"tke:" + hex(planRef) + ":" + kind + ":" + hex(part) for each dynamic part.
//
// Every dynamic part is hex-encoded so the mapping is injective regardless of
// caller-controlled characters (plan refs, task IDs, scope IDs may contain
// ':'). The store's events.id PRIMARY KEY is therefore the uniqueness
// constraint for every logical key: a second writer appending the same key
// gets ErrDuplicate (identical payload -> success; different payload ->
// ErrConflict).
func eventID(planRef, kind string, parts ...string) string {
	id := "tke:" + hex.EncodeToString([]byte(planRef)) + ":" + kind
	for _, part := range parts {
		id += ":" + hex.EncodeToString([]byte(part))
	}
	return id
}

// planStoredPayload is the tks_plan_stored event payload. The store stamps
// CreatedAt at append time; the plan record keeps the caller's own CreatedAt.
type planStoredPayload struct {
	Plan      Plan      `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

// planBoundPayload is the tks_plan_bound event payload (a scope re-binding).
type planBoundPayload struct {
	Scope     Scope     `json:"scope"`
	CreatedAt time.Time `json:"created_at"`
}

// taskCreatedPayload is the tks_task_created event payload.
type taskCreatedPayload struct {
	Task      Task      `json:"task"`
	CreatedAt time.Time `json:"created_at"`
}

// taskTransitionedPayload is the tks_task_transitioned event payload. ONE
// event carries the status change AND its journal timestamp; the ordered
// event log is the audit trail. TaskID is embedded so the payload is
// self-describing when replayed.
type taskTransitionedPayload struct {
	TaskID     string    `json:"task_id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	CreatedAt  time.Time `json:"created_at"`
}
