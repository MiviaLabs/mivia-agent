package ledger

// Integration tests — Phase 2 exit criterion. These tests exercise the full
// synthetic workflow lifecycle over a SQLite-backed store: admission of a
// canonical snapshot, a crashed holder's claim fencing a second executor,
// operator force-release, resume planning, interrupted/succeeded attempt
// outcomes, and a durable reopen that must reproduce the same snapshot and
// one complete audit trail.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestIntegrationSnapshotDigestStability: MarshalSnapshot of the same logical
// Snapshot rebuilt in two different map insertion orders yields byte-identical
// canonical JSON and the same digest. The snapshot digest is the admission
// fingerprint of a run, so it must be a pure function of the snapshot content.
func TestIntegrationSnapshotDigestStability(t *testing.T) {
	ref := func(digest string, content []byte) RefSnapshot {
		return RefSnapshot{Digest: digest, Bytes: content}
	}
	mk := func(inputs map[string]string, agents map[string]AgentSnapshot, schemas, templates map[string]RefSnapshot) Snapshot {
		return Snapshot{
			SchemaVersion:    SnapshotSchemaVersion,
			DefinitionTOML:   []byte("name = \"demo\"\n"),
			DefinitionDigest: "compiled-digest",
			Inputs:           inputs,
			Agents:           agents,
			Schemas:          schemas,
			Templates:        templates,
			Delivery:         &DeliverySnapshot{Mode: "draft", Provider: "github"},
		}
	}

	// Identical content, different map insertion orders.
	a := mk(
		map[string]string{"task": "add retries", "branch": "main", "zeta": "last"},
		map[string]AgentSnapshot{"engineer": {Digest: "agent-digest"}, "reviewer": {Digest: "reviewer-digest"}},
		map[string]RefSnapshot{"plan": ref("schema-digest", []byte(`{"type":"object"}`)), "run": ref("run-schema-digest", []byte(`{"type":"object"}`))},
		map[string]RefSnapshot{"plan": ref("tpl-digest", []byte("{{ inputs.task }}")), "summary": ref("summary-tpl-digest", []byte("{{ inputs.branch }}"))},
	)
	b := mk(
		map[string]string{"zeta": "last", "branch": "main", "task": "add retries"},
		map[string]AgentSnapshot{"reviewer": {Digest: "reviewer-digest"}, "engineer": {Digest: "agent-digest"}},
		map[string]RefSnapshot{"run": ref("run-schema-digest", []byte(`{"type":"object"}`)), "plan": ref("schema-digest", []byte(`{"type":"object"}`))},
		map[string]RefSnapshot{"summary": ref("summary-tpl-digest", []byte("{{ inputs.branch }}")), "plan": ref("tpl-digest", []byte("{{ inputs.task }}"))},
	)

	dataA, err := MarshalSnapshot(a)
	if err != nil {
		t.Fatalf("MarshalSnapshot(a): %v", err)
	}
	dataB, err := MarshalSnapshot(b)
	if err != nil {
		t.Fatalf("MarshalSnapshot(b): %v", err)
	}
	if !bytes.Equal(dataA, dataB) {
		t.Fatalf("insertion-order variants marshaled differently:\nA: %s\nB: %s", dataA, dataB)
	}
	if got, want := SnapshotDigest(dataA), SnapshotDigest(dataB); got != want {
		t.Fatalf("digest diverges across insertion orders: %q vs %q", got, want)
	}
}

// integrationSnapshot builds the realistic snapshot, pins MarshalSnapshot
// stability across calls and the digest = hex(sha256) rule. The marshaled
// output is the durable artifact: it must be stable across calls.
func integrationSnapshot(t *testing.T) (Snapshot, []byte) {
	t.Helper()
	snapshot := Snapshot{
		SchemaVersion:    SnapshotSchemaVersion,
		DefinitionTOML:   []byte("name = \"demo\"\n[run]\nmode = \"ci\"\n"),
		DefinitionDigest: "compiled-digest",
		Inputs:           map[string]string{"task": "add retries"},
		Agents:           map[string]AgentSnapshot{"engineer": {Digest: "agent-digest"}},
		Schemas:          map[string]RefSnapshot{"plan": {Digest: "schema-digest"}},
		Templates:        map[string]RefSnapshot{"plan": {Digest: "tpl-digest", Bytes: []byte("{{ inputs.task }}")}},
		Delivery:         &DeliverySnapshot{Mode: "draft", Provider: "github"},
	}
	snapshotJSON, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}
	again, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot (second): %v", err)
	}
	if !bytes.Equal(snapshotJSON, again) {
		t.Fatalf("MarshalSnapshot is not stable:\nfirst:  %s\nsecond: %s", snapshotJSON, again)
	}
	sum := sha256.Sum256(snapshotJSON)
	if want := hex.EncodeToString(sum[:]); SnapshotDigest(snapshotJSON) != want {
		t.Fatalf("SnapshotDigest = %q, want hex(sha256) %q", SnapshotDigest(snapshotJSON), want)
	}
	return snapshot, snapshotJSON
}

// integrationCreateRun admits the run on repoA: pending -> running, claims it
// for holderA and dispatches attempt 1.
func integrationCreateRun(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, snapshot Snapshot, snapshotJSON []byte) RunSnapshot {
	t.Helper()
	snap := RunSnapshot{
		RunID:          runID,
		WorkflowName:   "feature-delivery",
		WorkflowDigest: "wf-digest",
		SnapshotDigest: SnapshotDigest(snapshotJSON),
		InputDigest:    DigestHex([]byte(`{"task":"add retries"}`)),
		Status:         RunStatusPending,
		ActiveStepID:   "plan",
		BaseRef:        "main",
		BaseCommit:     "abc123def",
		WorktreeName:   "wf-feature-delivery-1",
		StartedAt:      fixedClock,
	}
	if err := repo.CreateRun(ctx, snap, snapshotJSON); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, 1, RunStatusRunning, nil); err != nil {
		t.Fatalf("pending->running: %v", err)
	}
	if err := repo.ClaimRun(ctx, runID, "wfh-a"); err != nil {
		t.Fatalf("ClaimRun(%q): %v", "wfh-a", err)
	}
	attempt1 := StepAttempt{
		AttemptID:        "wfa-1",
		RunID:            runID,
		StepID:           "plan",
		AttemptNo:        1,
		CoordinatorRunID: "run-abc",
		TaskID:           "task-1",
	}
	if err := repo.CreateStepAttempt(ctx, attempt1); err != nil {
		t.Fatalf("CreateStepAttempt(wfa-1): %v", err)
	}
	return snap
}

// integrationCrashResume pins the claim fence after the crash: the crashed
// holder's claim blocks a second executor until the operator force-releases
// it, then the claim is acquirable.
func integrationCrashResume(t *testing.T, ctx context.Context, repo *StorageRepository, runID string) {
	t.Helper()
	if err := repo.ClaimRun(ctx, runID, "wfh-b"); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("ClaimRun after crash = %v, want ErrClaimHeld", err)
	}
	// Operator force-release, then the claim is acquirable.
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		t.Fatalf("ClearRunClaim: %v", err)
	}
	if err := repo.ClaimRun(ctx, runID, "wfh-b"); err != nil {
		t.Fatalf("ClaimRun after force-release: %v", err)
	}
}

// integrationMarkInterrupted records the crashed attempt as interrupted (an
// interrupted completion derives NO transition) and pins PlanResume before
// the retry.
func integrationMarkInterrupted(t *testing.T, ctx context.Context, repo *StorageRepository, runID string) {
	t.Helper()
	stored, err := repo.GetStepAttempt(ctx, runID, "wfa-1")
	if err != nil {
		t.Fatalf("GetStepAttempt(wfa-1): %v", err)
	}
	if stored.Version != 1 {
		t.Fatalf("attempt wfa-1 version = %d, want 1", stored.Version)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, "wfa-1", 1, AttemptOutcome{Status: AttemptStatusInterrupted}); err != nil {
		t.Fatalf("interrupt wfa-1: %v", err)
	}
	interrupted, err := repo.GetStepAttempt(ctx, runID, "wfa-1")
	if err != nil {
		t.Fatalf("GetStepAttempt(wfa-1) after interrupt: %v", err)
	}
	if interrupted.Status != AttemptStatusInterrupted {
		t.Fatalf("wfa-1 status = %q, want interrupted", interrupted.Status)
	}
	if trans, err := repo.ListTransitions(ctx, runID); err != nil {
		t.Fatalf("ListTransitions: %v", err)
	} else if len(trans) != 0 {
		t.Fatalf("interrupted completion derived %d transitions, want 0", len(trans))
	}
	plan, err := PlanResume(ctx, repo, runID)
	if err != nil {
		t.Fatalf("PlanResume: %v", err)
	}
	if plan.Terminal {
		t.Fatalf("plan.Terminal = true, want false (resumable)")
	}
	if len(plan.AttemptsInFlight) != 0 {
		t.Fatalf("plan.AttemptsInFlight = %v, want empty (wfa-1 recorded interrupted)", plan.AttemptsInFlight)
	}
	if plan.NextAttemptNo != 2 {
		t.Fatalf("plan.NextAttemptNo = %d, want 2", plan.NextAttemptNo)
	}
	if plan.Run.ActiveStepID != "plan" {
		t.Fatalf("plan.Run.ActiveStepID = %q, want plan", plan.Run.ActiveStepID)
	}
}

// integrationDispatchAndComplete dispatches attempt 2, routes it to "success"
// with output evidence, pins PlanResume's terminal derivation, and records
// the terminal status CAS. It returns the content reference and evidence.
func integrationDispatchAndComplete(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, resumeClock time.Time) (string, []byte) {
	t.Helper()
	attempt2 := StepAttempt{
		AttemptID:        "wfa-2",
		RunID:            runID,
		StepID:           "plan",
		AttemptNo:        2,
		CoordinatorRunID: "run-def",
		TaskID:           "task-9",
	}
	if err := repo.CreateStepAttempt(ctx, attempt2); err != nil {
		t.Fatalf("CreateStepAttempt(wfa-2): %v", err)
	}
	evidence := []byte(`{"plan":"...","summary":"..."}`)
	ref := sdkadapter.Mint(sdkadapter.KindOutput, evidence)
	if err := repo.StoreContent(ctx, ref, evidence); err != nil {
		t.Fatalf("StoreContent: %v", err)
	}
	outcome := AttemptOutcome{
		Status:          AttemptStatusSucceeded,
		OutputRef:       ref,
		OutputDigest:    DigestHex(evidence),
		ToStepID:        "success",
		TransitionIndex: 0,
		MatchDigest:     DigestHex([]byte(`{"status":"succeeded"}`)),
		DecisionJSON:    []byte(`{"route":"success"}`),
	}
	if err := repo.CompleteStepAttempt(ctx, runID, "wfa-2", 1, outcome); err != nil {
		t.Fatalf("complete wfa-2: %v", err)
	}
	plan, err := PlanResume(ctx, repo, runID)
	if err != nil {
		t.Fatalf("PlanResume after success: %v", err)
	}
	if !plan.Terminal {
		t.Fatalf("plan.Terminal = false, want true (derived step success)")
	}
	if plan.TerminalStatus != RunStatusSucceeded {
		t.Fatalf("plan.TerminalStatus = %q, want succeeded", plan.TerminalStatus)
	}
	fin := resumeClock
	if err := repo.CompareAndSetRunStatus(ctx, runID, 2, RunStatusSucceeded, &fin); err != nil {
		t.Fatalf("running->succeeded: %v", err)
	}
	return ref, evidence
}

// integrationAssertRunState pins the reopened run: byte-identical snapshot,
// succeeded status, derived "success" step, version 3 and the terminal
// FinishedAt.
func integrationAssertRunState(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, snapshotJSON []byte, fin time.Time) RunSnapshot {
	t.Helper()
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		t.Fatalf("GetRunSnapshot: %v", err)
	}
	if !bytes.Equal(raw, snapshotJSON) {
		t.Fatalf("GetRunSnapshot diverged:\ngot:  %s\nwant: %s", raw, snapshotJSON)
	}
	runC, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if runC.Status != RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", runC.Status)
	}
	if runC.ActiveStepID != "success" {
		t.Fatalf("run ActiveStepID = %q, want success", runC.ActiveStepID)
	}
	if runC.Version != 3 {
		t.Fatalf("run version = %d, want 3", runC.Version)
	}
	if runC.FinishedAt == nil || !runC.FinishedAt.Equal(fin) {
		t.Fatalf("run FinishedAt = %v, want %v", runC.FinishedAt, fin)
	}
	return runC
}

// integrationAssertAttempts pins the reopened attempt log: exactly two
// attempts, wfa-1 interrupted with JOIN evidence preserved, wfa-2 succeeded
// with output/route fields.
func integrationAssertAttempts(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, ref string, evidence []byte) []StepAttempt {
	t.Helper()
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatalf("ListStepAttempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want exactly 2", len(attempts))
	}
	if attempts[0].AttemptID != "wfa-1" || attempts[0].AttemptNo != 1 || attempts[0].Status != AttemptStatusInterrupted {
		t.Fatalf("attempt[0] = %+v, want wfa-1 attempt_no 1 interrupted", attempts[0])
	}
	if attempts[0].CoordinatorRunID != "run-abc" || attempts[0].TaskID != "task-1" {
		t.Fatalf("wfa-1 JOIN evidence = (%q, %q), want (run-abc, task-1)",
			attempts[0].CoordinatorRunID, attempts[0].TaskID)
	}
	a2 := attempts[1]
	if a2.AttemptID != "wfa-2" || a2.AttemptNo != 2 || a2.Status != AttemptStatusSucceeded {
		t.Fatalf("attempt[1] = %+v, want wfa-2 attempt_no 2 succeeded", a2)
	}
	if a2.CoordinatorRunID != "run-def" || a2.TaskID != "task-9" {
		t.Fatalf("wfa-2 JOIN evidence = (%q, %q), want (run-def, task-9)",
			a2.CoordinatorRunID, a2.TaskID)
	}
	if a2.OutputRef != ref || a2.OutputDigest != DigestHex(evidence) {
		t.Fatalf("wfa-2 output = (%q, %q), want (%q, %q)", a2.OutputRef, a2.OutputDigest, ref, DigestHex(evidence))
	}
	if a2.ToStepID != "success" || a2.TransitionIndex != 0 || a2.MatchDigest != DigestHex([]byte(`{"status":"succeeded"}`)) {
		t.Fatalf("wfa-2 route = (%q, %d, %q), want (success, 0, match-digest)",
			a2.ToStepID, a2.TransitionIndex, a2.MatchDigest)
	}
	if !bytes.Equal(a2.DecisionJSON, []byte(`{"route":"success"}`)) {
		t.Fatalf("wfa-2 DecisionJSON = %s, want %s", a2.DecisionJSON, []byte(`{"route":"success"}`))
	}
	return attempts
}

// integrationAssertTransitions pins the reopened transition log: exactly one
// transition, from wfa-2 to "success".
func integrationAssertTransitions(t *testing.T, ctx context.Context, repo *StorageRepository, runID string) []TransitionRecord {
	t.Helper()
	trans, err := repo.ListTransitions(ctx, runID)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(trans) != 1 {
		t.Fatalf("transitions = %d, want exactly 1", len(trans))
	}
	if trans[0].FromAttemptID != "wfa-2" || trans[0].ToStepID != "success" {
		t.Fatalf("transition = %+v, want {wfa-2 -> success}", trans[0])
	}
	return trans
}

// integrationAssertAuditTrail pins the ONE COMPLETE AUDIT TRAIL: 7 events,
// kinds in order, sequences 1..7 with no gaps or duplicates, event IDs
// unique.
func integrationAssertAuditTrail(t *testing.T, store storage.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	events, err := store.Events(ctx, runID)
	if err != nil {
		t.Fatalf("storeC.Events: %v", err)
	}
	wantKinds := []string{
		eventKindRunCreated,
		eventKindRunStatusChanged,
		eventKindAttemptStarted,
		eventKindAttemptCompleted,
		eventKindAttemptStarted,
		eventKindAttemptCompleted,
		eventKindRunStatusChanged,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("events = %d, want %d", len(events), len(wantKinds))
	}
	seen := make(map[string]bool, len(events))
	for i, ev := range events {
		if ev.Kind != wantKinds[i] {
			t.Errorf("event %d kind = %q, want %q", i, ev.Kind, wantKinds[i])
		}
		if ev.Sequence != i+1 {
			t.Errorf("event %d sequence = %d, want %d (no gaps/duplicates)", i, ev.Sequence, i+1)
		}
		if seen[ev.ID] {
			t.Errorf("event ID %q duplicated at index %d", ev.ID, i)
		}
		seen[ev.ID] = true
	}
}

// integrationAssertNoDoubleDispatch pins the no-double-dispatch rule on the
// reopened run: the same run and the same (run, step, attempt_no) triple are
// rejected even under a different attempt ID.
func integrationAssertNoDoubleDispatch(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, snap RunSnapshot, snapshotJSON []byte) {
	t.Helper()
	if err := repo.CreateRun(ctx, snap, snapshotJSON); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second CreateRun = %v, want ErrDuplicate", err)
	}
	if err := repo.CreateStepAttempt(ctx, StepAttempt{
		AttemptID: "wfa-3", RunID: runID, StepID: "plan", AttemptNo: 2,
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate (plan, 2) triple = %v, want ErrDuplicate", err)
	}
}

// integrationAssertContent pins the content round-trip: the output evidence
// survives under its reference.
func integrationAssertContent(t *testing.T, ctx context.Context, repo *StorageRepository, ref string, evidence []byte) {
	t.Helper()
	got, err := repo.LoadContent(ctx, ref)
	if err != nil {
		t.Fatalf("LoadContent: %v", err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatalf("LoadContent = %s, want %s", got, evidence)
	}
}

// integrationAssertIndependentRebuild pins the digest-determinism rule on a
// second fresh executor: two independent rebuilds derive byte-identical
// state (rebuild is a pure function of the store).
func integrationAssertIndependentRebuild(t *testing.T, ctx context.Context, repo *StorageRepository, runID string, runC RunSnapshot, attempts []StepAttempt, trans []TransitionRecord) {
	t.Helper()
	runD, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("repoD.GetRun: %v", err)
	}
	if !reflect.DeepEqual(runC, runD) {
		t.Fatalf("GetRun diverges between independent rebuilds:\nC: %+v\nD: %+v", runC, runD)
	}
	attemptsD, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatalf("repoD.ListStepAttempts: %v", err)
	}
	if !reflect.DeepEqual(attempts, attemptsD) {
		t.Fatalf("ListStepAttempts diverges between independent rebuilds:\nC: %+v\nD: %+v", attempts, attemptsD)
	}
	transD, err := repo.ListTransitions(ctx, runID)
	if err != nil {
		t.Fatalf("repoD.ListTransitions: %v", err)
	}
	if !reflect.DeepEqual(trans, transD) {
		t.Fatalf("ListTransitions diverges between independent rebuilds:\nC: %+v\nD: %+v", trans, transD)
	}
}

// integrationAssertDurability pins the full reopened state: byte-identical
// snapshot, run fields, exactly two attempts, exactly one transition, the
// 7-event audit trail, no double dispatch and the content round-trip.
func integrationAssertDurability(t *testing.T, ctx context.Context, repo *StorageRepository, store storage.Store, runID string, snapshotJSON []byte, snap RunSnapshot, ref string, evidence []byte, fin time.Time) (RunSnapshot, []StepAttempt, []TransitionRecord) {
	t.Helper()
	runC := integrationAssertRunState(t, ctx, repo, runID, snapshotJSON, fin)
	attempts := integrationAssertAttempts(t, ctx, repo, runID, ref, evidence)
	trans := integrationAssertTransitions(t, ctx, repo, runID)
	integrationAssertAuditTrail(t, store, runID)
	integrationAssertNoDoubleDispatch(t, ctx, repo, runID, snap, snapshotJSON)
	integrationAssertContent(t, ctx, repo, ref, evidence)
	return runC, attempts, trans
}

// TestIntegrationInterruptedWorkflowResumes is the Phase 2 EXIT CRITERION: an
// interrupted synthetic workflow resumes with the same snapshot and one
// complete audit trail. A crashed holder's claim fences a second executor; the
// operator force-releases it; the resume records the crashed attempt as
// interrupted, dispatches attempt 2, routes to "success", records the terminal
// status CAS, and a fresh reopen must reproduce the same snapshot, exactly two
// attempts, exactly one transition and a 7-event audit trail with gapless
// sequences — the digest-determinism rule: rebuild is a pure function of the
// store.
func TestIntegrationInterruptedWorkflowResumes(t *testing.T) {
	ctx := context.Background()
	resumeClock := fixedClock.Add(time.Hour) // the resume happens later

	// --- Admission on repoA (SQLite backend) ---
	path := filepath.Join(t.TempDir(), "wf.db")
	storeA, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite A: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	repoA := NewStorageRepository(storeA)
	repoA.SetTimeSource(func() time.Time { return fixedClock })

	snapshot, snapshotJSON := integrationSnapshot(t)
	runID := "wfr-integration-1"
	snap := integrationCreateRun(t, ctx, repoA, runID, snapshot, snapshotJSON)

	// --- CRASH: repoA is abandoned WITHOUT Close(), so its claim survives. ---

	// The resume executor opens the same file later (clock advanced).
	storeB, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite B: %v", err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	repoB := NewStorageRepository(storeB)
	repoB.SetTimeSource(func() time.Time { return resumeClock })

	integrationCrashResume(t, ctx, repoB, runID)
	integrationMarkInterrupted(t, ctx, repoB, runID)
	ref, evidence := integrationDispatchAndComplete(t, ctx, repoB, runID, resumeClock)

	// --- DURABILITY: a fresh executor over the same file must see the same
	// snapshot and the one complete audit trail. ---
	storeC, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite C: %v", err)
	}
	t.Cleanup(func() { _ = storeC.Close() })
	repoC := NewStorageRepository(storeC)

	runC, attempts, trans := integrationAssertDurability(t, ctx, repoC, storeC, runID, snapshotJSON, snap, ref, evidence, resumeClock)

	// TWO INDEPENDENT REBUILDS AGREE: repoD opens the same file and must
	// derive byte-identical state (rebuild is a pure function of the store).
	storeD, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite D: %v", err)
	}
	t.Cleanup(func() { _ = storeD.Close() })
	repoD := NewStorageRepository(storeD)

	integrationAssertIndependentRebuild(t, ctx, repoD, runID, runC, attempts, trans)

	// The resume never re-executed step "plan" attempt 1: its record is
	// interrupted and the run's ONLY completed transition came from wfa-2.
	if attempts[0].Status != AttemptStatusInterrupted {
		t.Fatalf("wfa-1 status = %q, want interrupted (never re-executed)", attempts[0].Status)
	}
	if len(trans) != 1 || trans[0].FromAttemptID != "wfa-2" {
		t.Fatalf("expected exactly one completed transition from wfa-2, got %+v", trans)
	}
}
