package ledger

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// fixedTime returns a deterministic instant so rebuild assertions never depend
// on time.Now(): every timestamp must come from an event payload.
func fixedTime(sec int) time.Time {
	return time.Date(2025, 1, 2, 3, 4, sec, 0, time.UTC)
}

// mustMarshal builds wf_ payload JSON directly with encoding/json. The
// events.go marshal helpers are stubbed, so tests construct payloads from the
// declared payload structs' json tags.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

func wfEvent(id string, rowID uint64, seq int, kind string, payload []byte) storage.Event {
	return storage.Event{ID: id, RunID: "wfr-test-run-1", RowID: rowID, Sequence: seq, Kind: kind, Payload: payload}
}

func requireRun(t *testing.T, p Projection) *RunSnapshot {
	t.Helper()
	if p.Run == nil {
		t.Fatalf("Projection.Run is nil, want a run snapshot")
	}
	return p.Run
}

func requireAttempt(t *testing.T, p Projection, attemptID string) StepAttempt {
	t.Helper()
	for _, a := range p.Attempts {
		if a.AttemptID == attemptID {
			return a
		}
	}
	t.Fatalf("no attempt %q in %+v", attemptID, p.Attempts)
	return StepAttempt{}
}

func findLoopCounter(p Projection, name string) (LoopCounter, bool) {
	for _, l := range p.LoopCounters {
		if l.LoopName == name {
			return l, true
		}
	}
	return LoopCounter{}, false
}

// projRunCreatedPayload builds a wf_run_created payload with the given
// initial step.
func projRunCreatedPayload(t *testing.T, initialStep string) []byte {
	t.Helper()
	return mustMarshal(t, runCreatedPayload{
		Run: RunSnapshot{
			RunID: "wfr-test-run-1", WorkflowName: "wf", Status: RunStatusPending,
			ActiveStepID: initialStep, Version: 1, StartedAt: fixedTime(1),
		},
		SnapshotJSON: []byte(`{"schema_version":1}`),
		CreatedAt:    fixedTime(1),
	})
}

// projAttemptStartedPayload builds a wf_attempt_started payload for one
// attempt on the given step.
func projAttemptStartedPayload(t *testing.T, attemptID, stepID string) []byte {
	t.Helper()
	return mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: attemptID, RunID: "wfr-test-run-1", StepID: stepID, AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(1), Version: 1,
		},
		CreatedAt: fixedTime(1),
	})
}

// projAttemptCompletedPayload builds a succeeded wf_attempt_completed payload
// routed to the given step.
func projAttemptCompletedPayload(t *testing.T, attemptID, toStepID string) []byte {
	t.Helper()
	return mustMarshal(t, attemptCompletedPayload{
		AttemptID: attemptID, Status: AttemptStatusSucceeded, ToStepID: toStepID,
		TransitionIndex: 0, FinishedAt: fixedTime(2), CreatedAt: fixedTime(2),
	})
}

// projLoopIncrementedPayload builds a wf_loop_incremented payload.
func projLoopIncrementedPayload(t *testing.T) []byte {
	t.Helper()
	return mustMarshal(t, loopIncrementedPayload{LoopName: "build", Iterations: 1, CreatedAt: fixedTime(3)})
}

// mergedAttemptEvents builds the started+completed event pair for one attempt
// that completed with a route.
func mergedAttemptEvents(t *testing.T) []storage.Event {
	t.Helper()
	started := mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: "a1", RunID: "wfr-test-run-1", StepID: "plan", AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(1), Version: 1,
		},
		CreatedAt: fixedTime(1),
	})
	completed := mustMarshal(t, attemptCompletedPayload{
		AttemptID:       "a1",
		Status:          AttemptStatusSucceeded,
		OutputRef:       "ref:output:abc123",
		OutputDigest:    "sha256:deadbeef",
		ToStepID:        "implement",
		TransitionIndex: 2,
		MatchDigest:     "md-route-7",
		DecisionJSON:    []byte(`{"choice":"b","reason":"spec"}`),
		FinishedAt:      fixedTime(2),
		CreatedAt:       fixedTime(3),
	})
	return []storage.Event{
		wfEvent("e-started", 1, 1, eventKindAttemptStarted, started),
		wfEvent("e-completed", 2, 2, eventKindAttemptCompleted, completed),
	}
}

// TestRebuildProjectionOrdersByRowIDThenSequence covers requirement 1: events
// passed in scrambled order are folded by (RowID, Sequence) — a wf_run_created
// with RowID 2 is applied AFTER an attempt event with RowID 1; final state
// reflects the highest-sequence values; attempts are listed in event order.
func TestRebuildProjectionOrdersByRowIDThenSequence(t *testing.T) {
	created := mustMarshal(t, runCreatedPayload{
		Run: RunSnapshot{
			RunID: "wfr-test-run-1", WorkflowName: "wf", Status: RunStatusPending,
			ActiveStepID: "kickoff", Version: 1, StartedAt: fixedTime(1),
		},
		SnapshotJSON: []byte(`{"schema_version":1}`),
		CreatedAt:    fixedTime(2),
	})
	startedA1 := mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: "a1", RunID: "wfr-test-run-1", StepID: "plan", AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(3), Version: 1,
		},
		CreatedAt: fixedTime(3),
	})
	completedA1 := mustMarshal(t, attemptCompletedPayload{
		AttemptID: "a1", Status: AttemptStatusSucceeded, ToStepID: "implement",
		TransitionIndex: 0, MatchDigest: "md1", DecisionJSON: []byte(`{"step":"implement"}`),
		FinishedAt: fixedTime(4), CreatedAt: fixedTime(4),
	})
	startedA2 := mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: "a2", RunID: "wfr-test-run-1", StepID: "implement", AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(5), Version: 1,
		},
		CreatedAt: fixedTime(5),
	})

	// Scrambled on purpose: (RowID, Sequence) order is [created(1,1),
	// startedA1(2,2), startedA2(3,3), completedA1(4,4)].
	events := []storage.Event{
		wfEvent("e-completed-a1", 4, 4, eventKindAttemptCompleted, completedA1),
		wfEvent("e-started-a1", 2, 2, eventKindAttemptStarted, startedA1),
		wfEvent("e-created", 1, 1, eventKindRunCreated, created),
		wfEvent("e-started-a2", 3, 3, eventKindAttemptStarted, startedA2),
	}

	proj, err := RebuildProjection(events)
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if !proj.HasRun {
		t.Errorf("HasRun = false, want true after wf_run_created")
	}
	run := requireRun(t, proj)
	if run.RunID != "wfr-test-run-1" {
		t.Errorf("Run.RunID = %q, want wfr-test-run-1", run.RunID)
	}
	if len(proj.Attempts) != 2 {
		t.Fatalf("len(Attempts) = %d, want 2: %+v", len(proj.Attempts), proj.Attempts)
	}
	if proj.Attempts[0].AttemptID != "a1" || proj.Attempts[1].AttemptID != "a2" {
		t.Errorf("Attempts order = [%s, %s], want [a1, a2] (event order)", proj.Attempts[0].AttemptID, proj.Attempts[1].AttemptID)
	}
	// Highest sequence wins per attempt.
	if proj.Attempts[0].Status != AttemptStatusSucceeded {
		t.Errorf("a1 Status = %q, want succeeded (completion seq 4 beats start seq 2)", proj.Attempts[0].Status)
	}
	if proj.Attempts[0].ToStepID != "implement" {
		t.Errorf("a1 ToStepID = %q, want implement", proj.Attempts[0].ToStepID)
	}
	if proj.Attempts[1].Status != AttemptStatusRunning {
		t.Errorf("a2 Status = %q, want running", proj.Attempts[1].Status)
	}
	if proj.ActiveStepID != "implement" {
		t.Errorf("ActiveStepID = %q, want implement (newest step-bearing event)", proj.ActiveStepID)
	}
}

// TestRebuildProjectionIgnoresUnknownKinds covers requirement 2: coordinator
// kinds ("run_created") and unknown wf_ kinds are skipped without error, and
// HasRun stays false when only foreign events exist.
func TestRebuildProjectionIgnoresUnknownKinds(t *testing.T) {
	events := []storage.Event{
		{ID: "coord-run-created", RunID: "run-1", RowID: 1, Sequence: 1, Kind: "run_created", Payload: []byte(`{"run":{}}`)},
		{ID: "wf-unknown", RunID: "wfr-test-run-1", RowID: 2, Sequence: 2, Kind: "wf_totally_unknown", Payload: []byte(`{"anything":true}`)},
	}
	proj, err := RebuildProjection(events)
	if err != nil {
		t.Fatalf("RebuildProjection with only foreign/unknown kinds: %v", err)
	}
	if proj.HasRun {
		t.Errorf("HasRun = true, want false: foreign coordinator events must be ignored")
	}
	if proj.Run != nil {
		t.Errorf("Run = %+v, want nil", proj.Run)
	}
	if len(proj.Attempts) != 0 {
		t.Errorf("len(Attempts) = %d, want 0", len(proj.Attempts))
	}
}

// TestRebuildProjectionTimestampsFromPayloads covers requirement 3: timestamps
// are rebuilt from payload fields (CreatedAt -> Run.StartedAt, FinishedAt ->
// attempt.FinishedAt), never derived at read time.
func TestRebuildProjectionTimestampsFromPayloads(t *testing.T) {
	t1 := fixedTime(10) // run created at
	t2 := fixedTime(20) // attempt finished at

	created := mustMarshal(t, runCreatedPayload{
		Run: RunSnapshot{
			RunID: "wfr-test-run-1", WorkflowName: "wf", Status: RunStatusPending,
			ActiveStepID: "kickoff", Version: 1,
			StartedAt: time.Time{}, // must NOT leak into the rebuild
		},
		SnapshotJSON: []byte(`{"schema_version":1}`),
		CreatedAt:    t1,
	})
	started := mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: "a1", RunID: "wfr-test-run-1", StepID: "plan", AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(15), Version: 1,
		},
		CreatedAt: fixedTime(15),
	})
	completed := mustMarshal(t, attemptCompletedPayload{
		AttemptID: "a1", Status: AttemptStatusSucceeded, ToStepID: "implement",
		FinishedAt: t2, CreatedAt: fixedTime(21),
	})

	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-created", 1, 1, eventKindRunCreated, created),
		wfEvent("e-started", 2, 2, eventKindAttemptStarted, started),
		wfEvent("e-completed", 3, 3, eventKindAttemptCompleted, completed),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	run := requireRun(t, proj)
	if !run.StartedAt.Equal(t1) {
		t.Errorf("Run.StartedAt = %v, want payload CreatedAt %v (never time.Now())", run.StartedAt, t1)
	}
	if !bytes.Equal(proj.SnapshotJSON, []byte(`{"schema_version":1}`)) {
		t.Errorf("SnapshotJSON = %q, want canonical blob", proj.SnapshotJSON)
	}
	a := requireAttempt(t, proj, "a1")
	if a.FinishedAt == nil {
		t.Fatalf("a1.FinishedAt = nil, want %v", t2)
	}
	if !a.FinishedAt.Equal(t2) {
		t.Errorf("a1.FinishedAt = %v, want payload FinishedAt %v", a.FinishedAt, t2)
	}
}

// TestRebuildProjectionMergesAttemptsByID covers requirement 4: a started and
// a completed event for the SAME attempt_id yield ONE attempt carrying the
// completed status/fields (version becomes the completion's mutation, 2).
func TestRebuildProjectionMergesAttemptsByID(t *testing.T) {
	proj, err := RebuildProjection(mergedAttemptEvents(t))
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.Attempts) != 1 {
		t.Fatalf("len(Attempts) = %d, want 1 (merge by attempt_id): %+v", len(proj.Attempts), proj.Attempts)
	}
	a := proj.Attempts[0]
	if a.AttemptID != "a1" {
		t.Errorf("AttemptID = %q, want a1", a.AttemptID)
	}
	if a.Status != AttemptStatusSucceeded {
		t.Errorf("Status = %q, want succeeded", a.Status)
	}
	if a.OutputRef != "ref:output:abc123" {
		t.Errorf("OutputRef = %q, want ref:output:abc123", a.OutputRef)
	}
	if a.OutputDigest != "sha256:deadbeef" {
		t.Errorf("OutputDigest = %q, want sha256:deadbeef", a.OutputDigest)
	}
	if a.ToStepID != "implement" {
		t.Errorf("ToStepID = %q, want implement", a.ToStepID)
	}
	if a.TransitionIndex != 2 {
		t.Errorf("TransitionIndex = %d, want 2", a.TransitionIndex)
	}
	if a.MatchDigest != "md-route-7" {
		t.Errorf("MatchDigest = %q, want md-route-7", a.MatchDigest)
	}
	if !bytes.Equal(a.DecisionJSON, []byte(`{"choice":"b","reason":"spec"}`)) {
		t.Errorf("DecisionJSON = %q, want {\"choice\":\"b\",\"reason\":\"spec\"}", a.DecisionJSON)
	}
	if a.Version != 2 {
		t.Errorf("Version = %d, want 2 (started=1, completion=2)", a.Version)
	}
	// Fields from the started event are preserved.
	if a.StepID != "plan" || a.AttemptNo != 1 {
		t.Errorf("StepID/AttemptNo = %q/%d, want plan/1", a.StepID, a.AttemptNo)
	}
	if !a.StartedAt.Equal(fixedTime(1)) {
		t.Errorf("StartedAt = %v, want %v", a.StartedAt, fixedTime(1))
	}
	if a.FinishedAt == nil || !a.FinishedAt.Equal(fixedTime(2)) {
		t.Errorf("FinishedAt = %v, want %v", a.FinishedAt, fixedTime(2))
	}
}

// TestRebuildProjectionTransitionDerivation covers requirement 4: a completed
// attempt with ToStepID != "" derives exactly one TransitionRecord carrying
// the route fields, in ListTransitions order.
func TestRebuildProjectionTransitionDerivation(t *testing.T) {
	proj, err := RebuildProjection(mergedAttemptEvents(t))
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	// Derived transition, in ListTransitions order.
	if len(proj.Transitions) != 1 {
		t.Fatalf("len(Transitions) = %d, want 1: %+v", len(proj.Transitions), proj.Transitions)
	}
	tr := proj.Transitions[0]
	if tr.RunID != "wfr-test-run-1" {
		t.Errorf("Transition.RunID = %q, want wfr-test-run-1", tr.RunID)
	}
	if tr.FromAttemptID != "a1" {
		t.Errorf("Transition.FromAttemptID = %q, want a1", tr.FromAttemptID)
	}
	if tr.ToStepID != "implement" {
		t.Errorf("Transition.ToStepID = %q, want implement", tr.ToStepID)
	}
	if tr.TransitionIndex != 2 {
		t.Errorf("Transition.TransitionIndex = %d, want 2", tr.TransitionIndex)
	}
	if tr.MatchDigest != "md-route-7" {
		t.Errorf("Transition.MatchDigest = %q, want md-route-7", tr.MatchDigest)
	}
	if !bytes.Equal(tr.DecisionJSON, []byte(`{"choice":"b","reason":"spec"}`)) {
		t.Errorf("Transition.DecisionJSON = %q, want {\"choice\":\"b\",\"reason\":\"spec\"}", tr.DecisionJSON)
	}
	if !tr.CreatedAt.Equal(fixedTime(3)) {
		t.Errorf("Transition.CreatedAt = %v, want completion payload CreatedAt %v", tr.CreatedAt, fixedTime(3))
	}
}

// TestRebuildProjectionInterruptedCompletionNoTransition covers requirement 4:
// a completion with empty ToStepID (interrupted) derives NO transition.
func TestRebuildProjectionInterruptedCompletionNoTransition(t *testing.T) {
	started := mustMarshal(t, attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID: "a1", RunID: "wfr-test-run-1", StepID: "plan", AttemptNo: 1,
			Status: AttemptStatusRunning, StartedAt: fixedTime(1), Version: 1,
		},
		CreatedAt: fixedTime(1),
	})
	completed := mustMarshal(t, attemptCompletedPayload{
		AttemptID: "a1", Status: AttemptStatusInterrupted,
		FinishedAt: fixedTime(2), CreatedAt: fixedTime(3),
	})

	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-started", 1, 1, eventKindAttemptStarted, started),
		wfEvent("e-completed", 2, 2, eventKindAttemptCompleted, completed),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.Transitions) != 0 {
		t.Errorf("len(Transitions) = %d, want 0 for interrupted completion with empty ToStepID", len(proj.Transitions))
	}
	a := requireAttempt(t, proj, "a1")
	if a.Status != AttemptStatusInterrupted {
		t.Errorf("a1 Status = %q, want interrupted", a.Status)
	}
	if a.FinishedAt == nil || !a.FinishedAt.Equal(fixedTime(2)) {
		t.Errorf("a1 FinishedAt = %v, want %v", a.FinishedAt, fixedTime(2))
	}
}

// TestRebuildProjectionActiveStepID covers requirement 5: (a) no attempts ->
// initial step from the run payload; (b) an attempt start carries the step.
func TestRebuildProjectionActiveStepID(t *testing.T) {
	t.Run("no attempts uses initial step from run payload", func(t *testing.T) {
		proj, err := RebuildProjection([]storage.Event{
			wfEvent("e-created", 1, 1, eventKindRunCreated, projRunCreatedPayload(t, "kickoff")),
		})
		if err != nil {
			t.Fatalf("RebuildProjection: %v", err)
		}
		if proj.ActiveStepID != "kickoff" {
			t.Errorf("ActiveStepID = %q, want kickoff", proj.ActiveStepID)
		}
	})

	t.Run("attempt start carries the step", func(t *testing.T) {
		proj, err := RebuildProjection([]storage.Event{
			wfEvent("e-created", 1, 1, eventKindRunCreated, projRunCreatedPayload(t, "kickoff")),
			wfEvent("e-started", 2, 2, eventKindAttemptStarted, projAttemptStartedPayload(t, "a1", "plan")),
		})
		if err != nil {
			t.Fatalf("RebuildProjection: %v", err)
		}
		if proj.ActiveStepID != "plan" {
			t.Errorf("ActiveStepID = %q, want plan", proj.ActiveStepID)
		}
	})
}

// TestRebuildProjectionActiveStepIDTerminal covers requirement 5: (c) a
// completion target wins even over a later loop event (loop events carry no
// step and are skipped); (d) a completion to a terminal step wins.
func TestRebuildProjectionActiveStepIDTerminal(t *testing.T) {
	loop := projLoopIncrementedPayload(t)

	t.Run("completion target beats later loop event", func(t *testing.T) {
		proj, err := RebuildProjection([]storage.Event{
			wfEvent("e-created", 1, 1, eventKindRunCreated, projRunCreatedPayload(t, "kickoff")),
			wfEvent("e-started", 2, 2, eventKindAttemptStarted, projAttemptStartedPayload(t, "a1", "plan")),
			wfEvent("e-completed", 3, 3, eventKindAttemptCompleted, projAttemptCompletedPayload(t, "a1", "implement")),
			wfEvent("e-loop", 4, 4, eventKindLoopIncremented, loop),
		})
		if err != nil {
			t.Fatalf("RebuildProjection: %v", err)
		}
		if proj.ActiveStepID != "implement" {
			t.Errorf("ActiveStepID = %q, want implement (loop event carries no step)", proj.ActiveStepID)
		}
	})

	t.Run("completion to terminal step wins", func(t *testing.T) {
		proj, err := RebuildProjection([]storage.Event{
			wfEvent("e-created", 1, 1, eventKindRunCreated, projRunCreatedPayload(t, "plan")),
			wfEvent("e-started", 2, 2, eventKindAttemptStarted, projAttemptStartedPayload(t, "a1", "plan")),
			wfEvent("e-completed", 3, 3, eventKindAttemptCompleted, projAttemptCompletedPayload(t, "a1", "success")),
		})
		if err != nil {
			t.Fatalf("RebuildProjection: %v", err)
		}
		if proj.ActiveStepID != "success" {
			t.Errorf("ActiveStepID = %q, want success", proj.ActiveStepID)
		}
	})
}

// TestRebuildProjectionRunStatusReplay covers requirement 6: status changes
// apply in event order with version and finished_at preserved; HasRun turns
// true once a wf_run_created is seen.
func TestRebuildProjectionRunStatusReplay(t *testing.T) {
	finished := fixedTime(9)
	created := mustMarshal(t, runCreatedPayload{
		Run: RunSnapshot{
			RunID: "wfr-test-run-1", WorkflowName: "wf", Status: RunStatusPending,
			ActiveStepID: "kickoff", Version: 1, StartedAt: fixedTime(1),
		},
		SnapshotJSON: []byte(`{"schema_version":1}`),
		CreatedAt:    fixedTime(1),
	})
	statusRunning := mustMarshal(t, runStatusChangedPayload{Status: RunStatusRunning, Version: 2, CreatedAt: fixedTime(2)})
	statusSucceeded := mustMarshal(t, runStatusChangedPayload{Status: RunStatusSucceeded, Version: 3, FinishedAt: &finished, CreatedAt: fixedTime(3)})

	// Scrambled: (RowID, Sequence) order is created, running, succeeded.
	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-status-succeeded", 3, 3, eventKindRunStatusChanged, statusSucceeded),
		wfEvent("e-created", 1, 1, eventKindRunCreated, created),
		wfEvent("e-status-running", 2, 2, eventKindRunStatusChanged, statusRunning),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if !proj.HasRun {
		t.Errorf("HasRun = false, want true after wf_run_created")
	}
	run := requireRun(t, proj)
	if run.Status != RunStatusSucceeded {
		t.Errorf("Run.Status = %q, want succeeded", run.Status)
	}
	if run.Version != 3 {
		t.Errorf("Run.Version = %d, want 3", run.Version)
	}
	if run.FinishedAt == nil || !run.FinishedAt.Equal(finished) {
		t.Errorf("Run.FinishedAt = %v, want %v", run.FinishedAt, finished)
	}
	if !run.StartedAt.Equal(fixedTime(1)) {
		t.Errorf("Run.StartedAt = %v, want %v", run.StartedAt, fixedTime(1))
	}
}

// TestRebuildProjectionRunNilWithoutRunCreated covers requirement 6: the
// projection has no Run and HasRun stays false when no wf_run_created exists.
func TestRebuildProjectionRunNilWithoutRunCreated(t *testing.T) {
	status := mustMarshal(t, runStatusChangedPayload{Status: RunStatusRunning, Version: 2, CreatedAt: fixedTime(1)})
	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-status", 1, 1, eventKindRunStatusChanged, status),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if proj.HasRun {
		t.Errorf("HasRun = true, want false without wf_run_created")
	}
	if proj.Run != nil {
		t.Errorf("Run = %+v, want nil without wf_run_created", proj.Run)
	}
}

// TestRebuildProjectionUndecodableKnownKindErrors covers requirement 7: an
// undecodable payload for a KNOWN kind is an error, never silently ignored.
func TestRebuildProjectionUndecodableKnownKindErrors(t *testing.T) {
	proj, err := RebuildProjection([]storage.Event{
		{ID: "e-bad", RunID: "wfr-test-run-1", RowID: 1, Sequence: 1, Kind: eventKindRunCreated, Payload: []byte("not-json")},
	})
	if err == nil {
		t.Fatalf("RebuildProjection returned nil error for undecodable %s payload, want error (proj=%+v)", eventKindRunCreated, proj)
	}
}

// TestRebuildProjectionLoopCounters covers requirement 8: two increments for
// the same loop name yield Iterations from the latest event; different loop
// names coexist.
func TestRebuildProjectionLoopCounters(t *testing.T) {
	events := []storage.Event{
		wfEvent("e-loop-1", 1, 1, eventKindLoopIncremented, mustMarshal(t, loopIncrementedPayload{LoopName: "build", Iterations: 1, CreatedAt: fixedTime(1)})),
		wfEvent("e-loop-2", 2, 2, eventKindLoopIncremented, mustMarshal(t, loopIncrementedPayload{LoopName: "build", Iterations: 3, CreatedAt: fixedTime(2)})),
		wfEvent("e-loop-3", 3, 3, eventKindLoopIncremented, mustMarshal(t, loopIncrementedPayload{LoopName: "test", Iterations: 2, CreatedAt: fixedTime(3)})),
	}
	proj, err := RebuildProjection(events)
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.LoopCounters) != 2 {
		t.Fatalf("len(LoopCounters) = %d, want 2: %+v", len(proj.LoopCounters), proj.LoopCounters)
	}
	build, ok := findLoopCounter(proj, "build")
	if !ok {
		t.Fatalf("no loop counter %q in %+v", "build", proj.LoopCounters)
	}
	if build.Iterations != 3 {
		t.Errorf("build.Iterations = %d, want 3 (latest event wins)", build.Iterations)
	}
	testLoop, ok := findLoopCounter(proj, "test")
	if !ok {
		t.Fatalf("no loop counter %q in %+v", "test", proj.LoopCounters)
	}
	if testLoop.Iterations != 2 {
		t.Errorf("test.Iterations = %d, want 2", testLoop.Iterations)
	}
}

// TestRebuildProjectionApprovalsMerge covers requirement 9: approval_created
// and approval_resolved merge into one record carrying the resolved fields.
func TestRebuildProjectionApprovalsMerge(t *testing.T) {
	createdAt := fixedTime(1)
	created := mustMarshal(t, approvalCreatedPayload{
		Approval: ApprovalRecord{
			ApprovalID: "ap1", RunID: "wfr-test-run-1", StepID: "plan", Status: "pending",
			EvidenceJSON: []byte(`{"inputs":{}}`), CreatedAt: createdAt,
		},
		CreatedAt: createdAt,
	})
	resolvedAt := fixedTime(2)
	resolved := mustMarshal(t, approvalResolvedPayload{
		ApprovalID: "ap1", Status: "approved", Actor: "alice", Reason: "looks good",
		ResolvedAt: resolvedAt, CreatedAt: fixedTime(2),
	})

	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-approval-created", 1, 1, eventKindApprovalCreated, created),
		wfEvent("e-approval-resolved", 2, 2, eventKindApprovalResolved, resolved),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.Approvals) != 1 {
		t.Fatalf("len(Approvals) = %d, want 1: %+v", len(proj.Approvals), proj.Approvals)
	}
	ap := proj.Approvals[0]
	if ap.ApprovalID != "ap1" {
		t.Errorf("ApprovalID = %q, want ap1", ap.ApprovalID)
	}
	if ap.Status != "approved" {
		t.Errorf("Status = %q, want approved", ap.Status)
	}
	if ap.Actor != "alice" {
		t.Errorf("Actor = %q, want alice", ap.Actor)
	}
	if ap.Reason != "looks good" {
		t.Errorf("Reason = %q, want looks good", ap.Reason)
	}
	if ap.StepID != "plan" {
		t.Errorf("StepID = %q, want plan (preserved from created)", ap.StepID)
	}
	if !ap.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v", ap.CreatedAt, createdAt)
	}
	if ap.ResolvedAt == nil || !ap.ResolvedAt.Equal(resolvedAt) {
		t.Errorf("ResolvedAt = %v, want %v", ap.ResolvedAt, resolvedAt)
	}
}

// TestRebuildProjectionDeliveriesLatestWins covers requirement 9: the latest
// delivery_upserted wins per idempotency key.
func TestRebuildProjectionDeliveriesLatestWins(t *testing.T) {
	first := mustMarshal(t, deliveryUpsertedPayload{
		Delivery: DeliveryRecord{
			RunID: "wfr-test-run-1", IdempotencyKey: "key-1", Mode: "pr", BaseRef: "main",
			HeadRef: "feature", Status: "pending", UpdatedAt: fixedTime(1),
		},
		CreatedAt: fixedTime(1),
	})
	second := mustMarshal(t, deliveryUpsertedPayload{
		Delivery: DeliveryRecord{
			RunID: "wfr-test-run-1", IdempotencyKey: "key-1", Mode: "pr", BaseRef: "main",
			HeadRef: "feature", Status: "delivered", CommitSHA: "abc123",
			URL: "https://example.com/pr/1", UpdatedAt: fixedTime(2),
		},
		CreatedAt: fixedTime(2),
	})
	other := mustMarshal(t, deliveryUpsertedPayload{
		Delivery: DeliveryRecord{
			RunID: "wfr-test-run-1", IdempotencyKey: "key-2", Mode: "push", BaseRef: "main",
			HeadRef: "feature", Status: "pending", UpdatedAt: fixedTime(3),
		},
		CreatedAt: fixedTime(3),
	})

	// Scrambled: (RowID, Sequence) order is first, second, other.
	proj, err := RebuildProjection([]storage.Event{
		wfEvent("e-delivery-2", 2, 2, eventKindDeliveryUpserted, second),
		wfEvent("e-delivery-1", 1, 1, eventKindDeliveryUpserted, first),
		wfEvent("e-delivery-3", 3, 3, eventKindDeliveryUpserted, other),
	})
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	if len(proj.Deliveries) != 2 {
		t.Fatalf("len(Deliveries) = %d, want 2: %+v", len(proj.Deliveries), proj.Deliveries)
	}
	for _, d := range proj.Deliveries {
		if d.IdempotencyKey == "key-1" {
			if d.Status != "delivered" {
				t.Errorf("key-1 Status = %q, want delivered (latest upsert wins)", d.Status)
			}
			if d.URL != "https://example.com/pr/1" {
				t.Errorf("key-1 URL = %q, want https://example.com/pr/1", d.URL)
			}
			if d.CommitSHA != "abc123" {
				t.Errorf("key-1 CommitSHA = %q, want abc123", d.CommitSHA)
			}
			if !d.UpdatedAt.Equal(fixedTime(2)) {
				t.Errorf("key-1 UpdatedAt = %v, want %v", d.UpdatedAt, fixedTime(2))
			}
		}
	}
}
