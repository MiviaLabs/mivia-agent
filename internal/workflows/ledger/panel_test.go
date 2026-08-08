package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func panelDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validPanelTask(name string) PanelTaskSpec {
	deadline := time.Now().Add(time.Minute).UTC()
	input := fmt.Sprintf(`{"input":%q}`, name)
	work := PanelTaskSpec{
		TaskName: name, InputRef: "ref:input:" + name, InputDigest: panelDigest(input), InputSchemaRef: "ref:input-schema:" + name, InputSchemaDigest: panelDigest(`{}`),
		Budget: 1, Scope: "panel", AgentName: "agent", AgentDigest: panelDigest("agent"), Skill: "skill", Provider: "provider", Model: "model",
		OutputSchemaDigest: panelDigest(`{}`), OutputSchemaRef: "ref:output-schema:" + name, Timeout: time.Second, DeadlineAt: deadline,
		WorkLimits:                    PanelWorkLimits{MaxTurns: 1, MaxPromptTokens: 1, MaxOutputTokens: 1, MaxOutputPerCall: 1, MaxToolCalls: 1, DeadlineAt: deadline},
		Policy:                        coordledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		CoordinatorRequestFingerprint: "sha256:" + panelDigest("request:"+name),
	}
	work.WorkFingerprint = work.workFingerprint()
	return work
}

func panelTaskWithID(t *testing.T, name, taskID string) PanelTaskSpec {
	t.Helper()
	work := validPanelTask(name)
	input := fmt.Sprintf(`{"input":%q}`, name)
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{{ID: taskID, Name: work.TaskName, Input: []byte(input), InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true}}, work.Policy)
	if err != nil {
		t.Fatal(err)
	}
	work.CoordinatorRequestFingerprint = fingerprint
	work.WorkFingerprint = work.workFingerprint()
	return work
}

func storePanelTask(t *testing.T, repo *StorageRepository, work PanelTaskSpec) {
	t.Helper()
	input := fmt.Sprintf(`{"input":%q}`, work.TaskName)
	for _, item := range []struct{ ref, data string }{{work.InputRef, input}, {work.InputSchemaRef, `{}`}, {work.OutputSchemaRef, `{}`}} {
		if err := repo.StoreContent(context.Background(), item.ref, []byte(item.data)); err != nil {
			t.Fatal(err)
		}
	}
}

func storePanelExecution(t *testing.T, repo *StorageRepository, panel *PanelExecution) {
	t.Helper()
	for _, member := range panel.Members {
		storePanelTask(t, repo, member.Work)
	}
}

func validPanelExecution(t *testing.T, runID, attemptID string) *PanelExecution {
	members := make([]PanelMemberExecution, 2)
	for i, memberID := range []string{"member-0", "member-1"} {
		childRun, childTask := PanelChildIDs(runID, attemptID, memberID)
		members[i] = PanelMemberExecution{MemberID: memberID, CoordinatorRunID: childRun, TaskID: childTask, Work: panelTaskWithID(t, memberID, childTask), Order: i}
	}
	synthesisRun, synthesisTask := PanelChildIDs(runID, attemptID, "synthesis")
	return &PanelExecution{Phase: PanelPhaseMembersAdmitted, Members: members, SynthesisRunID: synthesisRun, SynthesisTaskID: synthesisTask}
}

func validSynthesisTask(t *testing.T, runID, attemptID string) PanelTaskSpec {
	_, taskID := PanelChildIDs(runID, attemptID, "synthesis")
	return panelTaskWithID(t, "synthesis", taskID)
}

type panelPhaseComparer interface {
	CompareAndSetPanelPhase(context.Context, string, string, uint64, PanelPhase, PanelPhase, *PanelSynthesisExecution) error
}

func TestPanelPhaseCASRequiresClaimAndAdvancesVersion(t *testing.T) {
	repo := newMemoryRepo(t)
	cas, ok := any(repo).(panelPhaseComparer)
	if !ok {
		t.Fatal("Repository must implement CompareAndSetPanelPhase")
	}
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt-1", RunID: run, StepID: "review", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt-1")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	synthesis := &PanelSynthesisExecution{Work: validSynthesisTask(t, run, attempt.AttemptID)}
	storePanelTask(t, repo, synthesis.Work)
	if err := cas.CompareAndSetPanelPhase(ctx, run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseSynthesisAdmitted, synthesis); err == nil {
		t.Fatal("unbound context must fail")
	}
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	ctx = ContextWithClaimHolder(ctx, "holder")
	if err := cas.CompareAndSetPanelPhase(ctx, run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseSynthesisAdmitted, synthesis); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.PanelExecution.Phase != PanelPhaseSynthesisAdmitted {
		t.Fatalf("attempt = %+v", got)
	}
}

func TestPanelChildPrincipalIsDeterministic(t *testing.T) {
	first := PanelChildPrincipal("wfr-test")
	if first != PanelChildPrincipal("wfr-test") {
		t.Fatal("same workflow run must derive the same panel principal")
	}
	if first == PanelChildPrincipal("wfr-other") || first.Role != "workflow-panel" || first.SessionID == "" {
		t.Fatalf("unexpected principal: %+v", first)
	}
}

func TestPanelChildContextReplacesHostCaller(t *testing.T) {
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "host", Role: "host"})
	caller, ok := runtime.CallerFrom(ContextWithPanelChildPrincipal(ctx, "wfr-test"))
	if !ok || caller != PanelChildPrincipal("wfr-test") {
		t.Fatalf("caller = %+v, want panel principal", caller)
	}
}

func TestPanelChildIDsAreCanonicalAndDistinct(t *testing.T) {
	run, task := PanelChildIDs("wfr-test", "attempt", "member-0")
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(run[len("run-"):])
	if err != nil || len(decoded) != 16 || run[:4] != "run-" || task[:5] != "task-" {
		t.Fatalf("unexpected child IDs: %q, %q", run, task)
	}
	otherRun, otherTask := PanelChildIDs("wfr-test", "attempt", "member-1")
	if run == otherRun || task == otherTask {
		t.Fatal("distinct children must get distinct identifiers")
	}
}

func TestPanelTaskSpecRejectsIncompleteWork(t *testing.T) {
	if err := (PanelTaskSpec{}).Validate(); err == nil {
		t.Fatal("incomplete work must fail")
	}
}

func TestPanelTaskSpecRejectsChangedLimitsAndDeadline(t *testing.T) {
	work := panelTaskWithID(t, "member", "task-member")
	work.WorkLimits.MaxTurns++
	if err := work.Validate(); err == nil {
		t.Fatal("changed panel limits must fail the work fingerprint")
	}
	work = panelTaskWithID(t, "member", "task-member")
	work.DeadlineAt = work.DeadlineAt.Add(time.Second)
	if err := work.Validate(); err == nil {
		t.Fatal("changed panel deadline must fail the work fingerprint")
	}
}

func TestPanelAdmissionVerifiesReferencedContent(t *testing.T) {
	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	if err := repo.CreateStepAttempt(ctx, attempt); err == nil {
		t.Fatal("panel admission without referenced content must fail")
	}
	storePanelExecution(t, repo, attempt.PanelExecution)
	attempt.PanelExecution.Members[0].Work.InputDigest = panelDigest("wrong")
	if err := repo.CreateStepAttempt(ctx, attempt); err == nil {
		t.Fatal("panel admission with mismatched input digest must fail")
	}
	attempt = StepAttempt{AttemptID: "attempt-2", RunID: run, StepID: "panel", AttemptNo: 2, PanelExecution: validPanelExecution(t, run, "attempt-2")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	attempt.PanelExecution.Members[0].Work.CoordinatorRequestFingerprint = "sha256:" + panelDigest("unrelated")
	if err := repo.CreateStepAttempt(ctx, attempt); err == nil {
		t.Fatal("panel admission with unrelated coordinator fingerprint must fail")
	}
}

func TestStepAttemptCloneCopiesPanelExecution(t *testing.T) {
	original := StepAttempt{PanelExecution: &PanelExecution{Members: []PanelMemberExecution{{MemberID: "one", Work: PanelTaskSpec{DependsOn: []string{}}}}}}
	clone := original.Clone()
	clone.PanelExecution.Members[0].MemberID = "changed"
	clone.PanelExecution.Members[0].Work.DependsOn = append(clone.PanelExecution.Members[0].Work.DependsOn, "unexpected")
	if original.PanelExecution.Members[0].MemberID != "one" || len(original.PanelExecution.Members[0].Work.DependsOn) != 0 {
		t.Fatal("panel clone aliases durable state")
	}
}

func TestPanelPhaseCASRejectsStaleHolderAfterTakeover(t *testing.T) {
	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, run, "old"); err != nil {
		t.Fatal(err)
	}
	if err := repo.TakeoverRunClaim(ctx, run, "new"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetPanelPhase(ContextWithClaimHolder(ctx, "old"), run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseCancelPending, nil); err != ErrClaimHeld {
		t.Fatalf("error = %v, want ErrClaimHeld", err)
	}
}

func TestPanelReplayRejectsMalformedAndUnknownEvents(t *testing.T) {
	for name, event := range map[string]storage.Event{
		"malformed": {ID: "1", RunID: "run", Sequence: 1, Kind: eventKindPanelPhaseSet, Payload: []byte("{")},
		"unknown":   {ID: "2", RunID: "run", Sequence: 1, Kind: "wf_panel_future", Payload: []byte("{}")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RebuildProjection([]storage.Event{event}); err == nil {
				t.Fatal("panel replay must reject the event")
			}
		})
	}
}

func TestPanelReplayRejectsStalePhaseVersion(t *testing.T) {
	run, attemptID := "run", "attempt"
	attempt := StepAttempt{AttemptID: attemptID, RunID: run, StepID: "panel", AttemptNo: 1, Status: AttemptStatusRunning, Version: 1, PanelExecution: validPanelExecution(t, run, attemptID)}
	started, err := marshalAttemptStarted(attemptStartedPayload{Attempt: attempt})
	if err != nil {
		t.Fatal(err)
	}
	phase, err := marshalPanelPhase(panelPhasePayload{AttemptID: attemptID, Version: 1, Phase: PanelPhaseCancelPending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildProjection([]storage.Event{{ID: "started", RunID: run, Sequence: 1, Kind: eventKindAttemptStarted, Payload: started}, {ID: "phase", RunID: run, Sequence: 2, Kind: eventKindPanelPhaseSet, Payload: phase}}); err == nil {
		t.Fatal("stale phase version must fail replay")
	}
}

func TestPanelReplayRejectsMalformedInitialState(t *testing.T) {
	run, attemptID := "run", "attempt"
	panel := validPanelExecution(t, run, attemptID)
	panel.Members[0].CoordinatorRunID = "run-not-deterministic"
	payload, err := marshalAttemptStarted(attemptStartedPayload{Attempt: StepAttempt{AttemptID: attemptID, RunID: run, StepID: "panel", AttemptNo: 1, Status: AttemptStatusRunning, Version: 1, PanelExecution: panel}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildProjection([]storage.Event{{ID: "started", RunID: run, Sequence: 1, Kind: eventKindAttemptStarted, Payload: payload}}); err == nil {
		t.Fatal("malformed panel initial state must fail replay")
	}
}

func TestPanelRejectsReservedSynthesisMemberID(t *testing.T) {
	panel := validPanelExecution(t, "run", "attempt")
	panel.Members[0].MemberID = "synthesis"
	panel.Members[0].CoordinatorRunID, panel.Members[0].TaskID = PanelChildIDs("run", "attempt", "synthesis")
	if err := panel.validateInitial("run", "attempt"); err == nil {
		t.Fatal("reserved synthesis member ID must fail")
	}
}

func TestPanelPhaseThenCompletionAdvancesVersion(t *testing.T) {
	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	claimCtx := ContextWithClaimHolder(ctx, "holder")
	if err := repo.CompareAndSetPanelPhase(claimCtx, run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseCancelPending, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, run, attempt.AttemptID, 2, AttemptOutcome{Status: AttemptStatusCanceled}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil || got.Version != 3 || got.Status != AttemptStatusCanceled {
		t.Fatalf("attempt=%+v err=%v", got, err)
	}
}

func TestPanelCancelPreservesSynthesisWork(t *testing.T) {
	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	claimCtx := ContextWithClaimHolder(ctx, "holder")
	synthesis := &PanelSynthesisExecution{Work: validSynthesisTask(t, run, attempt.AttemptID)}
	storePanelTask(t, repo, synthesis.Work)
	if err := repo.CompareAndSetPanelPhase(claimCtx, run, attempt.AttemptID, 1, PanelPhaseMembersAdmitted, PanelPhaseSynthesisAdmitted, synthesis); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetPanelPhase(claimCtx, run, attempt.AttemptID, 2, PanelPhaseSynthesisAdmitted, PanelPhaseCancelPending, nil); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetStepAttempt(ctx, run, attempt.AttemptID)
	if err != nil || got.PanelExecution.Synthesis == nil || got.PanelExecution.Synthesis.Work.TaskName != "synthesis" {
		t.Fatalf("attempt=%+v err=%v", got, err)
	}
}

func TestPanelPhaseCASRejectsTerminalAttempt(t *testing.T) {
	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, run, attempt.AttemptID, 1, AttemptOutcome{Status: AttemptStatusCanceled}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, run, "holder"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetPanelPhase(ContextWithClaimHolder(ctx, "holder"), run, attempt.AttemptID, 2, PanelPhaseMembersAdmitted, PanelPhaseCancelPending, nil); err != ErrConflict {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}
