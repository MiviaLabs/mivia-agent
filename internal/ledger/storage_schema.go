package ledger

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Storage event kind constants used internally by StorageLedgerRepository
// to serialize ledger state into the append-only storage.Store.
const (
	storageKindRunCreated        = "run_created"
	storageKindRunDeleted        = storage.KindRunDeleted
	storageKindRunStatusChanged  = "run_status_changed"
	storageKindTaskCreated       = "task_created"
	storageKindTaskStatusChanged = "task_status_changed"
	storageKindTaskOutputSet     = "task_output_set"
	storageKindTaskAttempt       = "task_attempt"
	storageKindLifecycleEvent    = "lifecycle_event"
	storageKindRunClosed         = "run_closed"
)

// ---------------------------------------------------------------------------
// Marshal helpers - pure functions, no state
// ---------------------------------------------------------------------------

func marshalRunSnapshot(snap RunSnapshot) ([]byte, error) {
	return json.Marshal(snap)
}

func unmarshalRunSnapshot(data []byte) (RunSnapshot, error) {
	var snap RunSnapshot
	err := json.Unmarshal(data, &snap)
	return snap, err
}

func marshalTaskSnapshot(snap TaskSnapshot) ([]byte, error) {
	return json.Marshal(snap)
}

func unmarshalTaskSnapshot(data []byte) (TaskSnapshot, error) {
	var snap TaskSnapshot
	err := json.Unmarshal(data, &snap)
	return snap, err
}

func marshalLifecycleEvent(evt LifecycleEvent) ([]byte, error) {
	return json.Marshal(evt)
}

func unmarshalLifecycleEvent(data []byte) (LifecycleEvent, error) {
	var evt LifecycleEvent
	err := json.Unmarshal(data, &evt)
	return evt, err
}

// taskStatusChangePayload is the JSON shape for task_status_changed events.
type taskStatusChangePayload struct {
	TaskID      string     `json:"task_id"`
	Status      string     `json:"status"`
	Version     uint64     `json:"version"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func marshalStatusChange(taskID, status string, version uint64, completedAt *time.Time) ([]byte, error) {
	return json.Marshal(taskStatusChangePayload{
		TaskID:      taskID,
		Status:      status,
		Version:     version,
		CompletedAt: completedAt,
	})
}

func unmarshalStatusChange(data []byte) (taskID, status string, version uint64, completedAt *time.Time, err error) {
	var p taskStatusChangePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", "", 0, nil, err
	}
	return p.TaskID, p.Status, p.Version, p.CompletedAt, nil
}

// taskOutputSetPayload is the JSON shape for task_output_set events.
type taskOutputSetPayload struct {
	TaskID    string `json:"task_id"`
	OutputRef string `json:"output_ref"`
	ErrorRef  string `json:"error_ref"`
}

func marshalOutputRefs(taskID, outputRef, errorRef string) ([]byte, error) {
	return json.Marshal(taskOutputSetPayload{
		TaskID:    taskID,
		OutputRef: outputRef,
		ErrorRef:  errorRef,
	})
}

func unmarshalOutputRefs(data []byte) (taskID, outputRef, errorRef string, err error) {
	var p taskOutputSetPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", "", "", err
	}
	return p.TaskID, p.OutputRef, p.ErrorRef, nil
}

// taskAttemptPayload is the JSON shape for task_attempt events.
type taskAttemptPayload struct {
	TaskID     string     `json:"task_id"`
	AttemptID  string     `json:"attempt_id"`
	Status     string     `json:"status"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func marshalAttemptEntry(taskID, attemptID, status string, finishedAt *time.Time) ([]byte, error) {
	return json.Marshal(taskAttemptPayload{
		TaskID:     taskID,
		AttemptID:  attemptID,
		Status:     status,
		FinishedAt: finishedAt,
	})
}

func unmarshalAttemptEntry(data []byte) (taskID, attemptID, status string, finishedAt *time.Time, err error) {
	var p taskAttemptPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", "", "", nil, err
	}
	return p.TaskID, p.AttemptID, p.Status, p.FinishedAt, nil
}

// runStatusChangePayload is the JSON shape for run_status_changed events.
type runStatusChangePayload struct {
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func marshalRunStatusChange(status string, completedAt *time.Time) ([]byte, error) {
	return json.Marshal(runStatusChangePayload{
		Status:      status,
		CompletedAt: completedAt,
	})
}

func unmarshalRunStatusChange(data []byte) (status string, completedAt *time.Time, err error) {
	var p runStatusChangePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil, err
	}
	return p.Status, p.CompletedAt, nil
}

// runClosedPayload is the JSON shape for run_closed events. Status and
// CompletedAt are optional: CloseRun carries the terminal transition (status
// canceled, completed_at) inside the single closure row, so the closure and
// the terminal status land in ONE fenced append. Rows written before the
// fields existed - or a hand-edited row - decode with zero values and close
// through closeRebuiltRun exactly as they always did.
type runClosedPayload struct {
	Status      string     `json:"status,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func marshalRunClosed(status string, completedAt *time.Time) ([]byte, error) {
	return json.Marshal(runClosedPayload{Status: status, CompletedAt: completedAt})
}

// unmarshalRunClosed decodes a run_closed payload. It is deliberately lenient:
// closure is the load-bearing fact and the optional status/completed_at fields
// are a refinement, so a payload this package cannot decode still closes the
// run through closeRebuiltRun (the legacy behavior) instead of wedging
// catch-up. Unknown extra fields are ignored, as json.Unmarshal does for any
// struct.
func unmarshalRunClosed(data []byte) (status string, completedAt *time.Time) {
	var p runClosedPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", nil
	}
	return p.Status, p.CompletedAt
}

// ---------------------------------------------------------------------------
// Projection rebuild - deterministic replay
// ---------------------------------------------------------------------------

// RebuildProjection replays storage events in sequence order to reconstruct
// the current RunSnapshot, task list, and lifecycle events. It is a pure
// deterministic function: the same event slice always produces the same state.
// Returns zero values if events is empty.
func RebuildProjection(events []storage.Event) (RunSnapshot, []TaskSnapshot, []LifecycleEvent, error) {
	if len(events) == 0 {
		return RunSnapshot{}, nil, nil, nil
	}

	var runSnap RunSnapshot
	tasksMap := make(map[string]TaskSnapshot)
	var lifecycleEvents []LifecycleEvent

	for _, evt := range events {
		switch evt.Kind {
		case storageKindRunDeleted:
			runSnap = RunSnapshot{}
			tasksMap = make(map[string]TaskSnapshot)
			lifecycleEvents = nil

		case storageKindRunCreated:
			if err := applyRebuiltRunCreated(&runSnap, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindRunStatusChanged:
			if err := applyRebuiltRunStatusChanged(&runSnap, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindTaskCreated:
			if err := applyRebuiltTaskCreated(tasksMap, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindTaskStatusChanged:
			if err := rebuildTaskStatus(tasksMap, runSnap.RunID, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindTaskOutputSet:
			if err := applyRebuiltTaskOutputSet(tasksMap, runSnap.RunID, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindTaskAttempt:
			if err := rebuildTaskAttempt(tasksMap, runSnap.RunID, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindLifecycleEvent:
			var err error
			lifecycleEvents, err = appendRebuiltLifecycleEvent(lifecycleEvents, evt)
			if err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindRunClosed:
			applyRebuiltRunClosed(&runSnap, evt.Payload)

		default:
		}
	}

	return runSnap, sortedRebuiltTasks(tasksMap), lifecycleEvents, nil
}

// applyRebuiltRunCreated applies a run_created event to the rebuilt snapshot.
func applyRebuiltRunCreated(runSnap *RunSnapshot, payload []byte) error {
	snap, err := unmarshalRunSnapshot(payload)
	if err != nil {
		return err
	}
	*runSnap = snap
	return nil
}

// applyRebuiltRunStatusChanged applies a run_status_changed event to the
// rebuilt snapshot.
func applyRebuiltRunStatusChanged(runSnap *RunSnapshot, payload []byte) error {
	status, completedAt, err := unmarshalRunStatusChange(payload)
	if err != nil {
		return err
	}
	runSnap.Status = RunStatus(status)
	if completedAt != nil {
		t := *completedAt
		runSnap.CompletedAt = &t
	}
	return nil
}

// applyRebuiltTaskCreated applies a task_created event to the rebuilt task map.
func applyRebuiltTaskCreated(tasksMap map[string]TaskSnapshot, payload []byte) error {
	snap, err := unmarshalTaskSnapshot(payload)
	if err != nil {
		return err
	}
	tasksMap[snap.TaskID] = snap
	return nil
}

// applyRebuiltTaskOutputSet applies a task_output_set event to the rebuilt
// task map.
func applyRebuiltTaskOutputSet(tasksMap map[string]TaskSnapshot, runID string, payload []byte) error {
	taskID, outputRef, errorRef, err := unmarshalOutputRefs(payload)
	if err != nil {
		return err
	}
	task, ok := tasksMap[taskID]
	if !ok {
		task = TaskSnapshot{RunID: runID, TaskID: taskID}
	}
	task.OutputRef = outputRef
	task.ErrorRef = errorRef
	tasksMap[taskID] = task
	return nil
}

// applyRebuiltRunClosed applies a run_closed event to the rebuilt snapshot.
// Status and completed_at refine the closure; closeRebuiltRun is the fallback.
func applyRebuiltRunClosed(runSnap *RunSnapshot, payload []byte) {
	status, completedAt := unmarshalRunClosed(payload)
	closeRebuiltRun(runSnap)
	if status != "" {
		runSnap.Status = RunStatus(status)
	}
	if completedAt != nil {
		t := *completedAt
		runSnap.CompletedAt = &t
	}
}

func closeRebuiltRun(runSnap *RunSnapshot) {
	if runSnap.Status != RunStatusCompleted &&
		runSnap.Status != RunStatusFailed &&
		runSnap.Status != RunStatusCanceled {
		runSnap.Status = RunStatusCanceled
	}
}

func sortedRebuiltTasks(tasksMap map[string]TaskSnapshot) []TaskSnapshot {
	tasks := make([]TaskSnapshot, 0, len(tasksMap))
	for _, task := range tasksMap {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks
}

func appendRebuiltLifecycleEvent(events []LifecycleEvent, evt storage.Event) ([]LifecycleEvent, error) {
	lifecycleEvent, err := fromStorageEvent(evt)
	if err != nil {
		return nil, err
	}
	return append(events, lifecycleEvent), nil
}

func rebuildTaskAttempt(tasks map[string]TaskSnapshot, runID string, payload []byte) error {
	taskID, attemptID, status, finishedAt, err := unmarshalAttemptEntry(payload)
	if err != nil {
		return err
	}
	task, ok := tasks[taskID]
	if !ok {
		task = TaskSnapshot{RunID: runID, TaskID: taskID}
	}
	for i := range task.Attempts {
		if task.Attempts[i].AttemptID != attemptID {
			continue
		}
		task.Attempts[i].Status = status
		if finishedAt != nil {
			t := *finishedAt
			task.Attempts[i].FinishedAt = &t
		}
		tasks[taskID] = task
		return nil
	}
	attempt := AttemptSnapshot{AttemptID: attemptID, Status: status}
	if finishedAt != nil {
		t := *finishedAt
		attempt.FinishedAt = &t
	}
	task.Attempts = append(task.Attempts, attempt)
	tasks[taskID] = task
	return nil
}

func rebuildTaskStatus(tasks map[string]TaskSnapshot, runID string, payload []byte) error {
	taskID, status, version, completedAt, err := unmarshalStatusChange(payload)
	if err != nil {
		return err
	}
	task, ok := tasks[taskID]
	if !ok {
		task = TaskSnapshot{RunID: runID, TaskID: taskID}
	}
	task.Status = status
	task.Version = version
	if completedAt != nil {
		t := *completedAt
		task.CompletedAt = &t
	}
	tasks[taskID] = task
	return nil
}

// ---------------------------------------------------------------------------
// Converter helpers between LifecycleEvent and storage.Event
// ---------------------------------------------------------------------------

// fromStorageEvent converts a storage.Event back to a ledger LifecycleEvent.
//
// The row carries only ID, RunID and the payload; Kind, TaskID, AttemptID and
// CreatedAt live inside the payload, because AppendEvent marshals the whole event
// into it. They have to be decoded back out or a rebuilt projection returns events
// with an empty Kind - and a caller filtering by kind then matches nothing, which
// is indistinguishable from "no such events happened". That is the failure this
// decode exists to prevent, for every run the current process did not create.
//
// CreatedAt is recovered, and is durable as of plan 21: AppendEvent now stamps it
// before marshalling, so the stored payload holds the append instant, and
// mem.AppendEvent stamps only what arrives unstamped. A replayed event therefore
// reports when it happened rather than when it was read back. Pinned by
// TestListEventsPreserveOriginalTimestampAcrossRebuild and, for the statement
// ordering that makes it possible, TestAppendEventStampsBeforeMarshalling.
//
// Two fields are deliberately NOT recovered:
//
//   - Sequence stays derived. mem.AppendEvent numbers the run's events in replay
//     order, replay order is store append order, and for a serial writer that
//     reproduces the live numbering exactly. Trusting a payload sequence instead
//     would let a caller open gaps and duplicates in the projection.
//   - ID stays the storage row id. Restoring the caller's id looks tempting -
//     it would make a replayed event report the id the model originally saw - but
//     the coordinator mints event ids from a PROCESS-LOCAL counter (evt-1, evt-2,
//     … reset on restart, unlike run ids which are random). A resumed run would
//     then re-mint an id the replay had just restored, mem.AppendEvent would
//     reject it as a duplicate, and the event would vanish from the projection
//     while its store row persisted. Making event ids unguessable is the
//     prerequisite; see plan 21 correction C2.
//
// Rows written before plan 21 hold a zero CreatedAt. There is no schema version
// to gate on - no version table, no PRAGMA user_version - so they are recognised
// by content rather than by a version check: a zero arrives at mem.AppendEvent,
// which stamps what arrives unstamped, and the row replays to the read instant
// exactly as it always did. Pinned by
// TestLegacyRowWithoutTimestampFallsBackToReadInstant.
func fromStorageEvent(evt storage.Event) (LifecycleEvent, error) {
	if evt.Kind != storageKindLifecycleEvent {
		return LifecycleEvent{}, nil
	}
	l := LifecycleEvent{
		ID:    evt.ID,
		RunID: evt.RunID,
	}
	if len(evt.Payload) > 0 {
		l.Payload = make([]byte, len(evt.Payload))
		copy(l.Payload, evt.Payload)
	}
	if decoded, ok := decodeMarshalledLifecycleEvent(evt); ok {
		l.Kind = decoded.Kind
		l.TaskID = decoded.TaskID
		l.AttemptID = decoded.AttemptID
		l.Payload = decoded.Payload
		// Unconditional on purpose. A pre-plan-21 row decodes to a zero CreatedAt,
		// and l.CreatedAt is already zero, so there is nothing an IsZero guard here
		// would change - the fallback for those rows is mem.AppendEvent stamping
		// what arrives unstamped, not a branch in this function.
		l.CreatedAt = decoded.CreatedAt
	}
	return l, nil
}

// decodeMarshalledLifecycleEvent recovers the event this package marshalled into
// a row payload.
//
// A successful json.Unmarshal is not sufficient evidence on its own: any JSON
// object - or null - decodes into a zero LifecycleEvent without error, which
// would blank the fields for foreign or hand-edited data instead of leaving them
// as the columns had them. Requiring the decoded RunID to match the row's is the
// discriminator; it always holds for anything this package wrote, and fails for
// data it did not.
func decodeMarshalledLifecycleEvent(evt storage.Event) (LifecycleEvent, bool) {
	if len(evt.Payload) == 0 {
		return LifecycleEvent{}, false
	}
	decoded, err := unmarshalLifecycleEvent(evt.Payload)
	if err != nil || decoded.RunID != evt.RunID {
		return LifecycleEvent{}, false
	}
	return decoded, true
}
