package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestPostMessageFindingRoundTrip(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	// Handler that posts a finding then returns.
	_ = d.Register(runtime.Subagent, "finder", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"lock inversion at L42"}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)
	// Wire tool's initCoordinator path: store coordinator under d.
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "find-1", Name: "finder", AgentName: "finder"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Err != nil {
		t.Fatalf("result = %+v", result.Results)
	}
	msgs, err := c.ListRunMessages(context.Background(), result.Snapshot.RunID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Kind != agentmsg.KindFinding {
		t.Fatalf("msgs = %+v", msgs)
	}
	if !strings.Contains(msgs[0].Synopsis, "lock inversion") {
		t.Fatalf("synopsis = %q", msgs[0].Synopsis)
	}
	// Result envelope attachment
	idx := taskMessageIndex(context.Background(), repo, result.Snapshot.Tasks)
	if got := idx["find-1"]; len(got) != 1 || got[0].Kind != "finding" {
		t.Fatalf("envelope index = %+v", idx)
	}
}

func TestPostMessageQuestionTimeoutNoAnswer(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"which file?","wait_seconds":1}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "ask-1", Name: "asker", AgentName: "asker", Timeout: 5 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Err != nil {
		t.Fatalf("result = %+v err=%v", result.Results, result.Results[0].Err)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Results[0].Output, &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "no_answer" || body["reason"] != "timed_out" {
		t.Fatalf("body = %+v", body)
	}
	// Task should be running again (or completed) - not stuck in awaiting_input
	snap, _ := c.Inspect(context.Background(), h)
	for _, task := range snap.Tasks {
		if task.Status == string(ledger.TaskStatusAwaitingInput) {
			t.Fatal("task stuck in awaiting_input after timeout")
		}
	}
}

func TestPostMessageQuestionUserAnswer(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	type ids struct{ runID, taskID string }
	idCh := make(chan ids, 1)
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, _ := runtime.TaskIdentityFrom(ctx)
		select {
		case idCh <- ids{id.RunID, id.TaskID}:
		default:
		}
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"need input","wait_seconds":10}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "ask-2", Name: "asker", AgentName: "asker", Timeout: 15 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got ids
	select {
	case got = <-idCh:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	// Poll until parked (time.After is allowed; time.Sleep is not).
	parkedOK := false
	deadline := time.After(3 * time.Second)
	for !parkedOK {
		if c.CountPendingQuestions(got.runID, got.taskID) == 1 {
			parkedOK = true
			break
		}
		select {
		case <-deadline:
			t.Fatal("question never parked")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !c.DeliverAnswer(got.runID, got.taskID, "the answer is 42") {
		t.Fatal("DeliverAnswer failed")
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(result.Results[0].Output, &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "answered" || body["answer"] != "the answer is 42" {
		t.Fatalf("body = %+v", body)
	}
}

func TestInjectBaselineMessaging(t *testing.T) {
	full := tools.NewRegistry()
	post := &postMessageTool{cfg: config.DefaultSubagentConfig}
	full.Register(post)
	scoped := tools.ScopedRegistry(full, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: map[string]struct{}{"read_file": {}}, // post_message not allowlisted
	})
	if _, ok := scoped.Get(toolPostMessage); ok {
		t.Fatal("post_message should not pass allowlist alone")
	}
	injectBaselineMessaging(full, scoped, config.DefaultSubagentConfig, nil)
	if _, ok := scoped.Get(toolPostMessage); !ok {
		t.Fatal("baseline inject should add post_message")
	}
	// Opt-out via disallowed
	scoped2 := tools.ScopedRegistry(full, tools.ScopeOptions{
		Mode:      tools.ScopeSpawned,
		Allowlist: map[string]struct{}{"read_file": {}},
	})
	injectBaselineMessaging(full, scoped2, config.DefaultSubagentConfig, map[string]struct{}{toolPostMessage: {}})
	if _, ok := scoped2.Get(toolPostMessage); ok {
		t.Fatal("disallowed post_message must not be injected")
	}
}

func TestRunMessagesPrincipalGate(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	tool := &runMessagesTool{dispatcher: d, cfg: cfg, repo: repo}
	// No handle → unknown
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"run_id":"missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unknown run_id") {
		t.Fatalf("out = %q", out)
	}
	_ = c
}

func TestRegisterMessagingToolsAndRunMessagesBody(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	reg := tools.NewRegistry()
	cfg := config.DefaultSubagentConfig
	if err := registerMessagingTools(d, reg, cfg, repo); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get(toolPostMessage); !ok {
		t.Fatal("post_message missing")
	}
	if _, ok := reg.Get(toolRunMessages); !ok {
		t.Fatal("run_messages missing")
	}
	// Idempotent re-register
	if err := registerMessagingTools(d, reg, cfg, repo); err != nil {
		t.Fatal(err)
	}
	// inject nil / disabled / already-present paths
	injectBaselineMessaging(nil, reg, cfg, nil)
	disabled := cfg
	f := false
	disabled.Messaging.Enabled = &f
	injectBaselineMessaging(reg, tools.NewRegistry(), disabled, nil)
	injectBaselineMessaging(reg, reg, cfg, nil) // already present
}

func TestRunMessagesWithBody(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	c := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	_ = d.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}))
	// Create a run and post a finding via coordinator.
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	snap, _ := c.Inspect(context.Background(), h)
	runID, taskID := snap.RunID, snap.Tasks[0].TaskID
	msg, _ := agentmsg.NewMessage(runID, agentmsg.KindFinding,
		agentmsg.Party{TaskID: taskID}, agentmsg.Party{}, "full body text", nil, agentmsg.Options{ID: "msg-rm"})
	if err := c.PostTaskMessage(context.Background(), runID, taskID, msg); err != nil {
		t.Fatal(err)
	}
	// Register accessible handle for principal gate.
	caller := runtime.Caller{SessionID: "sess-rm"}
	ctx := runtime.ContextWithCaller(context.Background(), caller)
	runHandles.Store(runID, &orchestrationHandle{
		coord: c, handle: h, repo: repo, dispatcher: d,
		principal: orchestrationPrincipal{sessionID: "sess-rm"},
	})
	t.Cleanup(func() { runHandles.Delete(runID) })

	tool := &runMessagesTool{dispatcher: d, cfg: cfg, repo: repo}
	out, err := tool.Execute(ctx, json.RawMessage(`{"run_id":"`+runID+`","include_body":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "full body text") || !strings.Contains(out, "msg-rm") {
		t.Fatalf("out = %s", out)
	}
}

func TestPostMessageCancelWhileParked(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	type ids struct{ runID, taskID string }
	idCh := make(chan ids, 1)
	_ = d.Register(runtime.Subagent, "asker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		id, _ := runtime.TaskIdentityFrom(ctx)
		select {
		case idCh <- ids{id.RunID, id.TaskID}:
		default:
		}
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"question","body":"hanging","wait_seconds":30}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)
	coordinators.Store(d, c)
	coordinatorRepos.Store(d, repo)
	t.Cleanup(func() {
		coordinators.Delete(d)
		coordinatorRepos.Delete(d)
	})
	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "cancel-park", Name: "asker", AgentName: "asker", Timeout: 30 * time.Second},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got ids
	select {
	case got = <-idCh:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	// Wait until parked.
	deadline := time.After(3 * time.Second)
	for c.CountPendingQuestions(got.runID, got.taskID) != 1 {
		select {
		case <-deadline:
			t.Fatal("did not park")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := c.Cancel(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil && result == nil {
		t.Fatal(err)
	}
	// Task should not remain awaiting_input forever.
	snap, _ := c.Inspect(context.Background(), h)
	for _, task := range snap.Tasks {
		if task.Status == string(ledger.TaskStatusAwaitingInput) {
			t.Fatalf("task still awaiting_input after cancel: %s", task.Status)
		}
	}
}
