package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestEnsureNonInteractiveParentDeclinesParkImmediately pins the generic
// non-interactive-parent mechanism: a run created via EnsureRun with
// NonInteractiveParent set must decline a child's parked question IMMEDIATELY
// at park time (decline sentinel on the answer channel) instead of registering
// a park that would burn the asker's full wait_seconds, and the task must
// complete normally (never failed).
func TestEnsureNonInteractiveParentDeclinesParkImmediately(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	var (
		coord        Coordinator
		parkErr      error
		gotBody      string
		gotImmediate bool
		parked       bool
	)
	_ = d.Register(runtime.Subagent, "asker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, ok := runtime.TaskIdentityFrom(ctx)
		if !ok {
			return nil, fmt.Errorf("no task identity stamped")
		}
		start := time.Now()
		answerCh, unpark, err := coord.ParkQuestion(id.RunID, id.TaskID, "msg-ask-1", 30*time.Second)
		parkErr = err
		if err != nil {
			return nil, err
		}
		defer unpark()
		select {
		case body := <-answerCh:
			gotBody = body
			gotImmediate = time.Since(start) < time.Second
		case <-time.After(2 * time.Second):
			// The park was registered and consumed real wait time (the stall).
			parked = true
			gotImmediate = false
			return json.RawMessage(`{"parked":true}`), nil
		}
		return json.RawMessage(`{"parked":false}`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coord = New(repo, p)

	h, err := coord.EnsureRun(ctx, EnsureRunRequest{
		RunID:                NewRunID(),
		Tasks:                []subagents.Task{{ID: "asker-1", Name: "asker", AgentName: "asker"}},
		IdempotencyKey:       "non-interactive-step",
		NonInteractiveParent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := coord.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if parkErr != nil {
		t.Fatalf("ParkQuestion returned error: %v", parkErr)
	}
	if parked {
		t.Fatal("non-interactive parent must not register a real park")
	}
	if !gotImmediate {
		t.Fatalf("decline did not arrive immediately (parked=%v, body=%q)", parked, gotBody)
	}
	want := agentmsg.AskDeclinePrefix + agentmsg.DeclineReasonParentNonInteractive
	if gotBody != want {
		t.Fatalf("answer body = %q, want %q", gotBody, want)
	}
	snap, _ := coord.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID
	if n := coord.CountPendingQuestions(runID, taskID); n != 0 {
		t.Fatalf("CountPendingQuestions = %d, want 0 (no park / TTL consumed)", n)
	}
	if res.Err != nil {
		t.Fatalf("run result error = %v, want nil (task must not fail)", res.Err)
	}
	if len(res.Results) != 1 || res.Results[0].Err != nil || res.Results[0].Status != "completed" {
		t.Fatalf("task must complete without failure, results = %+v", res.Results)
	}
}

// TestParkQuestionWithoutNonInteractiveParentStillParks pins the inverse: a
// run created without the flag keeps the existing park semantics (a park is
// registered and waits for a real answer).
func TestParkQuestionWithoutNonInteractiveParentStillParks(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})

	var (
		coord         Coordinator
		handlerErr    error
		pendingAtPark int
		gotAnswer     string
	)
	_ = d.Register(runtime.Subagent, "asker", invoker(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, ok := runtime.TaskIdentityFrom(ctx)
		if !ok {
			return nil, fmt.Errorf("no task identity stamped")
		}
		answerCh, unpark, err := coord.ParkQuestion(id.RunID, id.TaskID, "msg-ask-1")
		if err != nil {
			handlerErr = err
			return nil, err
		}
		defer unpark()
		pendingAtPark = coord.CountPendingQuestions(id.RunID, id.TaskID)
		if !coord.DeliverAnswer(id.RunID, id.TaskID, "msg-ask-1", "yes") {
			handlerErr = fmt.Errorf("DeliverAnswer failed for a parked interactive question")
			return nil, handlerErr
		}
		select {
		case gotAnswer = <-answerCh:
		case <-time.After(2 * time.Second):
			handlerErr = fmt.Errorf("timeout waiting for the answer")
			return nil, handlerErr
		}
		return json.RawMessage(`{"answered":true}`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	coord = New(repo, p)

	h, err := coord.EnsureRun(ctx, EnsureRunRequest{
		RunID:          NewRunID(),
		Tasks:          []subagents.Task{{ID: "asker-2", Name: "asker", AgentName: "asker"}},
		IdempotencyKey: "interactive-step",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := coord.Join(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if pendingAtPark != 1 {
		t.Fatalf("CountPendingQuestions at park = %d, want 1", pendingAtPark)
	}
	if gotAnswer != "yes" {
		t.Fatalf("answer = %q, want %q", gotAnswer, "yes")
	}
	if res.Err != nil || len(res.Results) != 1 || res.Results[0].Err != nil {
		t.Fatalf("task must complete, results = %+v (err=%v)", res.Results, res.Err)
	}
}
