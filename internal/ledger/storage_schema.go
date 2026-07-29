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
	storageKindRunStatusChanged  = "run_status_changed"
	storageKindTaskCreated       = "task_created"
	storageKindTaskStatusChanged = "task_status_changed"
	storageKindTaskOutputSet     = "task_output_set"
	storageKindTaskAttempt       = "task_attempt"
	storageKindLifecycleEvent    = "lifecycle_event"
	storageKindRunClosed         = "run_closed"
)

// ---------------------------------------------------------------------------
// Marshal helpers — pure functions, no state
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

func marshalRunClosed() ([]byte, error) {
	return []byte("{}"), nil
}

// ---------------------------------------------------------------------------
// Projection rebuild — deterministic replay
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
		case storageKindRunCreated:
			snap, err := unmarshalRunSnapshot(evt.Payload)
			if err != nil {
				return RunSnapshot{}, nil, nil, err
			}
			runSnap = snap

		case storageKindRunStatusChanged:
			status, completedAt, err := unmarshalRunStatusChange(evt.Payload)
			if err != nil {
				return RunSnapshot{}, nil, nil, err
			}
			runSnap.Status = RunStatus(status)
			if completedAt != nil {
				t := *completedAt
				runSnap.CompletedAt = &t
			}

		case storageKindTaskCreated:
			snap, err := unmarshalTaskSnapshot(evt.Payload)
			if err != nil {
				return RunSnapshot{}, nil, nil, err
			}
			tasksMap[snap.TaskID] = snap

		case storageKindTaskStatusChanged:
			if err := rebuildTaskStatus(tasksMap, runSnap.RunID, evt.Payload); err != nil {
				return RunSnapshot{}, nil, nil, err
			}

		case storageKindTaskOutputSet:
			taskID, outputRef, errorRef, err := unmarshalOutputRefs(evt.Payload)
			if err != nil {
				return RunSnapshot{}, nil, nil, err
			}
			task, ok := tasksMap[taskID]
			if !ok {
				task = TaskSnapshot{RunID: runSnap.RunID, TaskID: taskID}
			}
			task.OutputRef = outputRef
			task.ErrorRef = errorRef
			tasksMap[taskID] = task

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
			closeRebuiltRun(&runSnap)

		default:
		}
	}

	return runSnap, sortedRebuiltTasks(tasksMap), lifecycleEvents, nil
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

// toStorageEvent converts a ledger LifecycleEvent to a storage.Event.
// The Sequence and CreatedAt fields are left zeroed — the store assigns sequence.
func toStorageEvent(evt LifecycleEvent) storage.Event {
	return storage.Event{
		ID:      evt.ID,
		RunID:   evt.RunID,
		Kind:    storageKindLifecycleEvent,
		Payload: evt.Payload,
	}
}

// fromStorageEvent converts a storage.Event back to a ledger LifecycleEvent.
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
	return l, nil
}
