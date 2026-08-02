package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestParkAndDeliverAnswer(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	answerCh, unpark, err := c.ParkQuestion("run-1", "task-1", "msg-q")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	if c.CountPendingQuestions("run-1", "task-1") != 1 {
		t.Fatal("expected one pending question")
	}
	if !c.DeliverAnswer("run-1", "task-1", "yes") {
		t.Fatal("DeliverAnswer failed")
	}
	select {
	case a := <-answerCh:
		if a != "yes" {
			t.Fatalf("answer = %q", a)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for answer")
	}
}

func TestMessageQuota(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	if err := c.ConsumeMessageQuota("r", "t", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeMessageQuota("r", "t", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeMessageQuota("r", "t", 2); err == nil {
		t.Fatal("expected quota exceeded")
	}
}

func TestAwaitingInputTransitionsOnCoordinator(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	// Seed a running task without full spawn (direct ledger).
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r-park", Status: ledger.RunStatusRunning})
	_ = repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "r-park", TaskID: "t-park", Status: string(ledger.TaskStatusRunning), Version: 1,
	})
	coord := c.(*coordinator)
	if err := coord.TransitionToAwaitingInput(ctx, "r-park", "t-park"); err != nil {
		t.Fatal(err)
	}
	snap, _ := repo.GetTask(ctx, "r-park", "t-park")
	if snap.Status != string(ledger.TaskStatusAwaitingInput) {
		t.Fatalf("status = %s", snap.Status)
	}
	if err := coord.TransitionFromAwaitingInput(ctx, "r-park", "t-park", string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	snap, _ = repo.GetTask(ctx, "r-park", "t-park")
	if snap.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("status = %s", snap.Status)
	}
}

func TestTaskIdentityStampedOnPoolContext(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	var saw runtime.TaskIdentity
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, ok := runtime.TaskIdentityFrom(ctx)
		if ok {
			saw = id
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "tid-1", Name: "worker", AgentName: "worker-agent"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if saw.TaskID != "tid-1" || saw.Agent != "worker-agent" {
		t.Fatalf("identity = %+v", saw)
	}
	if saw.RunID == "" {
		t.Fatal("runID empty")
	}
}

func TestListRunMessagesAfterPost(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{}, "found it", nil, agentmsg.Options{ID: "msg-list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListRunMessages(ctx, runID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].MessageID != "msg-list" || list[0].Synopsis != "found it" {
		t.Fatalf("list = %+v", list)
	}
}

func TestParkQuestionDuplicate(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	_, unpark, err := c.ParkQuestion("r", "t", "m1")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	if _, _, err := c.ParkQuestion("r", "t", "m2"); err == nil {
		t.Fatal("duplicate park must fail")
	}
}

func TestDeliverAnswerNoPendingAndDouble(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	if c.DeliverAnswer("r", "t", "x") {
		t.Fatal("no pending")
	}
	ch, unpark, err := c.ParkQuestion("r", "t", "m")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()
	if !c.DeliverAnswer("r", "t", "first") {
		t.Fatal("first deliver")
	}
	if c.DeliverAnswer("r", "t", "second") {
		t.Fatal("second deliver should fail (buffer full)")
	}
	<-ch
}

func TestTransitionAwaitingInputErrorPaths(t *testing.T) {
	c, repo := newPostMessageCoordinator(t)
	ctx := context.Background()
	coord := c.(*coordinator)
	if err := coord.TransitionToAwaitingInput(ctx, "missing", "t"); err == nil {
		t.Fatal("missing run/task")
	}
	_ = repo.CreateRun(ctx, "", ledger.RunSnapshot{RunID: "r2", Status: ledger.RunStatusRunning})
	_ = repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "r2", TaskID: "t2", Status: string(ledger.TaskStatusCompleted), Version: 1,
	})
	if err := coord.TransitionToAwaitingInput(ctx, "r2", "t2"); err == nil {
		t.Fatal("cannot park completed")
	}
	// Idempotent when already awaiting
	_ = repo.CompareAndSetTaskStatus(ctx, "r2", "t2", 1, string(ledger.TaskStatusRunning))
	// completed→running invalid; recreate
	_ = repo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID: "r2", TaskID: "t3", Status: string(ledger.TaskStatusRunning), Version: 1,
	})
	if err := coord.TransitionToAwaitingInput(ctx, "r2", "t3"); err != nil {
		t.Fatal(err)
	}
	if err := coord.TransitionToAwaitingInput(ctx, "r2", "t3"); err != nil {
		t.Fatal("already awaiting should be ok")
	}
	// From awaiting: resume
	if err := coord.TransitionFromAwaitingInput(ctx, "r2", "t3", string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	// Already running with same target → nil
	if err := coord.TransitionFromAwaitingInput(ctx, "r2", "t3", string(ledger.TaskStatusRunning)); err != nil {
		t.Fatal(err)
	}
	// Already running with different target → conflict
	if err := coord.TransitionFromAwaitingInput(ctx, "r2", "t3", string(ledger.TaskStatusCanceled)); err != ledger.ErrConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	if err := coord.TransitionFromAwaitingInput(ctx, "missing", "t", string(ledger.TaskStatusRunning)); err == nil {
		t.Fatal("missing task")
	}
}

func TestConsumeMessageQuotaUnlimited(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	for i := 0; i < 5; i++ {
		if err := c.ConsumeMessageQuota("r", "t", 0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadMessageBody(t *testing.T) {
	c, _ := newPostMessageCoordinator(t)
	ctx := context.Background()
	if _, err := c.LoadMessageBody(ctx, ""); err == nil {
		t.Fatal("empty ref")
	}
	h, err := c.Spawn(ctx, []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(ctx, h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(ctx, h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID
	msg, _ := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{}, "body-x", nil, agentmsg.Options{ID: "msg-body"})
	if err := c.PostTaskMessage(ctx, runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	list, _ := c.ListRunMessages(ctx, runID, taskID)
	if len(list) != 1 {
		t.Fatal(list)
	}
	full, err := c.LoadMessageBody(ctx, list[0].ContentRef)
	if err != nil || full.Body != "body-x" {
		t.Fatalf("full=%+v err=%v", full, err)
	}
	if _, err := c.ListRunMessages(ctx, "", ""); err == nil {
		t.Fatal("empty run_id")
	}
}

// handlerFunc adapts a function to runtime.Handler for tests.
type handlerFunc func(context.Context, runtime.Request) (json.RawMessage, error)

func (f handlerFunc) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	return f(ctx, req)
}
