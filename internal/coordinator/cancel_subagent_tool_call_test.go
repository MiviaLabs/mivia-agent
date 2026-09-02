package coordinator

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// toolCallOnceCompleter plays a two-step SDK-backed subagent turn: the
// first ChatTurn returns exactly one tool call (a fixed, test-chosen ID and
// tool name, so the test can target CancelSubagentToolCall precisely), the
// second returns final text regardless of how the first tool call resolved
// - proving a canceled tool call folds into an ordinary (if failed) tool
// result rather than aborting the task, mirroring the fold-into-string
// contract slice 2a already established at the raw dispatcherShim layer.
type toolCallOnceCompleter struct {
	mu       sync.Mutex
	calls    int
	callID   string
	toolName string
}

func (c *toolCallOnceCompleter) Name() string { return "tool-call-once" }

func (c *toolCallOnceCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if n == 1 {
		call := provider.ToolCall{ID: c.callID, Type: "function"}
		call.Function.Name = c.toolName
		call.Function.Arguments = "{}"
		return &provider.Response{ToolCalls: []provider.ToolCall{call}, FinishReason: "tool_calls"}, nil
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

func (c *toolCallOnceCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil || r == nil {
		return "", err
	}
	return r.Content, nil
}

func (c *toolCallOnceCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

// subagentBlockingTool is a CLI tool that blocks in Execute until either its
// own call context is canceled (recording the error) or the test releases
// it (recording a nil error), so a test can distinguish "this call was
// actually canceled" from "this call ran to natural completion" after the
// fact.
type subagentBlockingTool struct {
	name    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu        sync.Mutex
	resultErr error
	ran       bool
}

func newSubagentBlockingTool(name string) *subagentBlockingTool {
	return &subagentBlockingTool{name: name, entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *subagentBlockingTool) Name() string               { return b.name }
func (b *subagentBlockingTool) Description() string        { return "blocks until canceled or released" }
func (b *subagentBlockingTool) Parameters() map[string]any { return map[string]any{"type": "object"} }

func (b *subagentBlockingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	b.once.Do(func() { close(b.entered) })
	var err error
	select {
	case <-b.release:
		err = nil
	case <-ctx.Done():
		err = ctx.Err()
	}
	b.mu.Lock()
	b.resultErr, b.ran = err, true
	b.mu.Unlock()
	if err != nil {
		return "", err
	}
	return "sibling-ok", nil
}

func (b *subagentBlockingTool) outcome() (ran bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ran, b.resultErr
}

// cancelableSubagentTask registers one MultiStepHandler-backed subagent task
// whose single tool call blocks until canceled or released. Its
// OnToolCancelReady hook mirrors the production wiring
// (cliorchestrate.ToolCancelReadyHook + SessionDispatcherOpts.OnToolCancelReady
// + registerMultiStepHandler/newMultiStepHandler): read the task identity
// off ctx and forward the published ToolCanceler to the coordinator's own
// registry. registered closes once that hand-off has happened, so a test
// can wait for it before calling CancelSubagentToolCall.
func cancelableSubagentTask(t *testing.T, d *runtime.Dispatcher, coord Coordinator, handlerName, callID string) (tool *subagentBlockingTool, registered <-chan struct{}) {
	t.Helper()
	tool = newSubagentBlockingTool(handlerName + "-tool")
	reg := tools.NewRegistry()
	reg.Register(tool)

	reg2 := make(chan struct{})
	var once sync.Once
	h := &subagents.MultiStepHandler{
		Completer:    &toolCallOnceCompleter{callID: callID, toolName: tool.Name()},
		FullRegistry: reg,
		SystemPrompt: "test subagent",
		MaxSteps:     4,
		OnToolCancelReady: func(ctx context.Context, canceler agent.ToolCanceler) {
			id, ok := runtime.TaskIdentityFrom(ctx)
			if !ok {
				return
			}
			coord.RegisterSubagentToolCanceler(id.RunID, id.TaskID, canceler)
			once.Do(func() { close(reg2) })
		},
	}
	if err := d.Register(runtime.Subagent, handlerName, h); err != nil {
		t.Fatalf("register handler %q: %v", handlerName, err)
	}
	return tool, reg2
}

// waitClosed fails the test if ch does not close within budget.
func waitClosed(t *testing.T, ch <-chan struct{}, budget time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(budget):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestCancelSubagentToolCall_IsolatedFromSiblingAndTask is the central
// regression test for slice 2c: canceling ONE tool call inside ONE running
// subagent task must (a) actually stop that in-flight call (proving the
// OnToolCancelReady hook fired and its ToolCanceler reached the coordinator
// end to end), (b) leave the task's SIBLING tool call - running inside a
// different subagent task in the same run - completely unaffected, and (c)
// leave both tasks free to finish normally (a canceled tool call folds into
// that task's own history as a failed result, it does not fail or cancel
// the task itself).
func TestCancelSubagentToolCall_IsolatedFromSiblingAndTask(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	p := subagents.New(d, subagents.Policy{Workers: 2})
	c := New(repo, p)
	coord := c.(*coordinator)

	toolA, registeredA := cancelableSubagentTask(t, d, coord, "blockA", "call-A")
	toolB, registeredB := cancelableSubagentTask(t, d, coord, "blockB", "call-B")

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "taskA", Name: "blockA", Input: json.RawMessage(`"do it"`)},
		{ID: "taskB", Name: "blockB", Input: json.RawMessage(`"do it"`)},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	waitClosed(t, toolA.entered, 5*time.Second, "task A's tool call to start")
	waitClosed(t, toolB.entered, 5*time.Second, "task B's tool call to start")
	waitClosed(t, registeredA, 5*time.Second, "task A's ToolCanceler registration")
	waitClosed(t, registeredB, 5*time.Second, "task B's ToolCanceler registration")

	// (a) cancel task A's in-flight tool call.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := coord.CancelSubagentToolCall(ctx, h, "taskA", "call-A")
	if err != nil {
		t.Fatalf("CancelSubagentToolCall: %v", err)
	}
	if !ok {
		t.Fatal("expected CancelSubagentToolCall to find and cancel the in-flight call")
	}

	// A repeat cancel of the same (now-finished) call must be a clean no-op.
	ok, err = coord.CancelSubagentToolCall(context.Background(), h, "taskA", "call-A")
	if err != nil {
		t.Fatalf("repeat CancelSubagentToolCall: %v", err)
	}
	if ok {
		t.Fatal("repeat CancelSubagentToolCall found a call that should already be finished")
	}

	// (b) task B's tool call must be untouched by A's cancel: give the
	// canceled call's effects time to (wrongly) propagate if they were going
	// to, then release B and confirm it ran to natural completion, never
	// observing ctx.Done().
	time.Sleep(50 * time.Millisecond)
	bSnapBefore, err := repo.GetTask(context.Background(), h.runID, "taskB")
	if err != nil {
		t.Fatal(err)
	}
	if bSnapBefore.Status != string(ledger.TaskStatusRunning) {
		t.Fatalf("task B status = %q, want running (must be unaffected by A's tool-call cancel)", bSnapBefore.Status)
	}
	close(toolB.release)

	// (c) neither task is canceled/failed by the tool-call cancel: both
	// reach "completed" via the completer's second-step final text.
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusForTaskID(result.Results, "taskA"); got != "completed" {
		t.Fatalf("task A result status = %q, want completed (a canceled TOOL CALL must not cancel/fail the TASK)", got)
	}
	if got := statusForTaskID(result.Results, "taskB"); got != "completed" {
		t.Fatalf("task B result status = %q, want completed", got)
	}

	if ran, resultErr := toolA.outcome(); !ran || resultErr == nil {
		t.Fatalf("task A's tool call outcome = ran=%v err=%v, want ran=true err=non-nil (actually canceled)", ran, resultErr)
	}
	if ran, resultErr := toolB.outcome(); !ran || resultErr != nil {
		t.Fatalf("task B's tool call outcome = ran=%v err=%v, want ran=true err=nil (never canceled)", ran, resultErr)
	}
}

// TestCancelSubagentToolCall_UnknownIsSafeNoop proves CancelSubagentToolCall
// is a safe no-op - no panic, no error, false result - for every shape of
// "nothing to cancel": an unknown task ID, and a task ID that IS registered
// but with an unknown/already-finished call ID.
func TestCancelSubagentToolCall_UnknownIsSafeNoop(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	coord := c.(*coordinator)

	tool, registered := cancelableSubagentTask(t, d, coord, "blockOnly", "call-only")

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "blockOnly", Input: json.RawMessage(`"do it"`)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, tool.entered, 5*time.Second, "the tool call to start")
	waitClosed(t, registered, 5*time.Second, "the ToolCanceler registration")

	// Unknown task ID entirely.
	ok, err := coord.CancelSubagentToolCall(context.Background(), h, "does-not-exist", "call-only")
	if err != nil {
		t.Fatalf("unknown task ID: got error %v, want nil", err)
	}
	if ok {
		t.Fatal("unknown task ID: expected false, got true")
	}

	// Known task, unknown call ID.
	ok, err = coord.CancelSubagentToolCall(context.Background(), h, "t1", "call-never-existed")
	if err != nil {
		t.Fatalf("unknown call ID: got error %v, want nil", err)
	}
	if ok {
		t.Fatal("unknown call ID: expected false, got true")
	}

	// Clean up: release the still-blocked tool call and let the run finish.
	close(tool.release)
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

// TestCancelSubagentToolCall_RecoveredHandleIsNoop proves a recovered
// handle - no live in-process owner, and so no live ToolCanceler ever
// registered against it in this process - is a safe no-op rather than a
// panic or a false claim of cancellation.
func TestCancelSubagentToolCall_RecoveredHandleIsNoop(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)
	coord := c.(*coordinator)
	recovered := &RunHandle{
		runID: "recovered-run", done: make(chan struct{}), cancelDone: make(chan struct{}),
		owner: coord,
	}
	ok, cancelErr := coord.CancelSubagentToolCall(context.Background(), recovered, "t1", "call-1")
	if cancelErr != nil {
		t.Fatalf("recovered handle: got error %v, want nil", cancelErr)
	}
	if ok {
		t.Fatal("recovered handle: expected false, got true")
	}
}
