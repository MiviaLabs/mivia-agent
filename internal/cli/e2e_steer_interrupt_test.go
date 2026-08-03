package cli

// Plan 54 §7 (cli/send_to_task e2e): mid-step steer delivery through the full
// coordinator path.
//
// TestSendToTaskInterruptBreaksIntoBlockedChild pins the interrupt contract:
// an urgent (Interrupt=true) steer breaks into a child whose FIRST ChatTurn
// blocks on ctx.Done() — the LLM-scoped context the steer watcher cancels —
// canceling ONLY that call; the loop soft-continues, BeforeStep drains the
// steer, and the second call sees the steer body in its request history and
// finishes normally.
//
// TestSteerLandsAtStepBoundaryUnchanged pins the plan 53.03 regression: a
// non-urgent (Interrupt=false) steer to a child whose first call is gate-
// blocked on a test-controlled release channel (NOT ctx.Done) never interrupts
// the call; the steer waits, the released call completes, and the next call
// receives the steer body at the step boundary.
//
// Both run through the real coordinator harness: coordinator.Spawn → pool
// ContextForTask stamps runtime.MailboxAccess (drain + interrupt + pending) →
// MultiStepHandler.applyMailboxAccess wires the steer watcher into the nested
// agent loop → coord.SendToTask enqueues the steer mid-run. Handshakes (the
// completer's started/gated signals) replace fixed sleeps wherever possible;
// the only bounded waits are the join window (positive "breaks in" assertion)
// and the boundary regression's short grace period (negative "not canceled"
// assertion).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

const (
	steerUrgentBody   = "URGENT: stop expanding scope"
	steerBoundaryBody = "REMEMBER: keep the scope tight"
	// steerSeenMarker is the final answer both completers emit after the second
	// call observes the injected steer; the tests assert it in the child's
	// completed result.
	steerSeenMarker = "SAW_STEER"
	// steerBoundaryGrace is the bounded window the boundary regression holds
	// the first call open after enqueuing the non-urgent steer, so any broken
	// cancel path has a chance to manifest BEFORE the gate is released (the
	// negative "never interrupts" assertion).
	steerBoundaryGrace = 250 * time.Millisecond
)

// steerBlockMode selects how the completer's first call blocks.
type steerBlockMode int

const (
	// blockOnLLMCtx blocks on ctx.Done() — the LLM-scoped context the steer
	// watcher cancels — and returns ctx.Err() (interrupt test).
	blockOnLLMCtx steerBlockMode = iota
	// blockOnGate blocks on a test-controlled release channel, never on
	// ctx.Done, so a non-urgent steer provably waits (boundary regression).
	blockOnGate
)

// steerInterruptCompleter scripts a multi-step subagent whose FIRST ChatTurn
// blocks so the test can land a parent steer mid-run. The second call checks
// its request history for the framed steer body (expectBody inside a
// <parent-message>) and finishes with steerSeenMarker.
//
// Handshake channels (all buffered, signaled non-blocking):
//   - started fires when any call begins — the test waits for it to know the
//     child is past step 1's BeforeStep and its steer watcher is live;
//   - gated fires (gate mode only) the moment the first call is actually
//     blocked on the release channel.
//
// All recorded state (calls, canceled, firstErr, steerSeen, requests) is
// guarded by mu; the loop goroutine writes it, the test goroutine reads it
// after Join, and the mutex keeps the -race run clean regardless of the exact
// happens-before edges.
type steerInterruptCompleter struct {
	mode       steerBlockMode
	started    chan struct{} // buffered; signaled when a call begins
	gated      chan struct{} // buffered; signaled when the gate-mode first call is blocked
	gate       chan struct{} // closed by the test to release the gate-mode first call
	expectBody string        // steer body the second call must see

	mu        sync.Mutex
	calls     int
	canceled  []int              // 0-based call indices that observed ctx cancellation
	firstErr  error              // ctx error the first call observed (blockOnLLMCtx)
	steerSeen bool               // a call saw expectBody inside a <parent-message> frame
	requests  []provider.Request // snapshot of every request, for diagnostics
}

func (c *steerInterruptCompleter) Name() string { return "steer-interrupt" }

func (c *steerInterruptCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *steerInterruptCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

func (c *steerInterruptCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	idx := c.calls
	c.calls++
	// Snapshot the request: the loop appends to the same Messages backing
	// array between calls, so a stored reference could be mutated later.
	req.Messages = append([]provider.Message(nil), req.Messages...)
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	if idx == 0 {
		return c.firstCall(ctx)
	}
	return c.finalCall(ctx, req)
}

// firstCall blocks the child's first LLM call so the test can land a steer
// mid-run. blockOnLLMCtx: the steer watcher's llmCancel() is the only thing
// that can unblock it, and the observed ctx error is recorded. blockOnGate:
// the call waits for the test's release channel (NOT ctx.Done) and returns a
// ping tool call so the loop takes another step and the steer drains at the
// boundary; any ctx cancellation is recorded as a regression failure.
func (c *steerInterruptCompleter) firstCall(ctx context.Context) (*provider.Response, error) {
	switch c.mode {
	case blockOnLLMCtx:
		<-ctx.Done()
		c.mu.Lock()
		c.firstErr = ctx.Err()
		c.canceled = append(c.canceled, 0)
		c.mu.Unlock()
		return nil, ctx.Err()
	case blockOnGate:
		select {
		case c.gated <- struct{}{}:
		default:
		}
		select {
		case <-c.gate:
		case <-ctx.Done():
			c.mu.Lock()
			c.canceled = append(c.canceled, 0)
			c.mu.Unlock()
			return nil, ctx.Err()
		}
		return &provider.Response{
			ToolCalls:    []provider.ToolCall{steerPingCall()},
			FinishReason: "tool_calls",
		}, nil
	}
	return nil, fmt.Errorf("unknown steer block mode %d", c.mode)
}

// finalCall is every call after the first: it records whether the steer body
// arrived in a framed <parent-message> and finishes with steerSeenMarker.
func (c *steerInterruptCompleter) finalCall(_ context.Context, req provider.Request) (*provider.Response, error) {
	c.mu.Lock()
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "<parent-message>") && strings.Contains(m.Content, c.expectBody) {
			c.steerSeen = true
		}
	}
	c.mu.Unlock()
	return &provider.Response{Content: steerSeenMarker, FinishReason: "stop"}, nil
}

// steerPingCall is a no-op tool call the gate-mode first call returns so the
// loop soft-continues to a second step where the boundary steer drains.
func steerPingCall() provider.ToolCall {
	var call provider.ToolCall
	call.ID = "steer-ping-1"
	call.Type = "function"
	call.Function.Name = "ping"
	call.Function.Arguments = `{}`
	return call
}

// spawnSteerInterruptChild builds the same coordinator harness the steer
// integration tests use: a MultiStepHandler over the test completer registered
// as a subagent, run through coordinator.Spawn so contextForTask stamps the
// MailboxAccess bundle (drain + interrupt + pending) that applyMailboxAccess
// turns into the steer watcher wiring. Returns the coordinator, run handle,
// runID, and taskID.
func spawnSteerInterruptChild(t *testing.T, comp *steerInterruptCompleter) (coordinator.Coordinator, *coordinator.RunHandle, string, string) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	reg := tools.NewRegistry()
	reg.Register(pingTool{})
	h := &subagents.MultiStepHandler{
		Completer: comp, FullRegistry: reg, Dispatcher: d,
		Model: "test-model", MaxSteps: 5, SystemPrompt: "You are a test agent.",
	}
	if err := d.Register(runtime.Subagent, "steered", h); err != nil {
		t.Fatalf("register subagent: %v", err)
	}
	coord := coordinator.New(repo, subagents.New(d, subagents.Policy{Workers: 1}))
	taskID := "steer-child"
	handle, err := coord.Spawn(context.Background(), []subagents.Task{
		{ID: taskID, Name: "steered", AgentName: "steered",
			Input: json.RawMessage(`"do work"`), Timeout: 10 * time.Second},
	}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = coord.Cancel(context.Background(), handle) })
	snap, err := coord.Inspect(context.Background(), handle)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return coord, handle, snap.RunID, taskID
}

// sendSteer enqueues a parent steer via SendToTask and returns delivered.
func sendSteer(t *testing.T, coord coordinator.Coordinator, handle *coordinator.RunHandle, runID, taskID, id, body string, interrupt bool) bool {
	t.Helper()
	msg, err := agentmsg.NewMessage(runID, agentmsg.KindSteer,
		agentmsg.Party{Role: agentmsg.ParentSentinel},
		agentmsg.Party{TaskID: taskID},
		body, nil,
		agentmsg.Options{ID: id, Interrupt: interrupt})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	delivered, err := coord.SendToTask(context.Background(), handle, taskID, msg)
	if err != nil {
		t.Fatalf("SendToTask: %v", err)
	}
	return delivered
}

// TestSendToTaskInterruptBreaksIntoBlockedChild: an urgent interrupt steer
// cancels ONLY the child's in-flight LLM call (its first ChatTurn, blocked on
// ctx.Done), the loop soft-continues, BeforeStep drains the steer, and the
// second call sees the steer body in its request history and completes with a
// non-error result.
func TestSendToTaskInterruptBreaksIntoBlockedChild(t *testing.T) {
	comp := &steerInterruptCompleter{
		mode:       blockOnLLMCtx,
		started:    make(chan struct{}, 4),
		expectBody: steerUrgentBody,
	}
	coord, handle, runID, taskID := spawnSteerInterruptChild(t, comp)

	// Handshake: wait until the child's FIRST ChatTurn is in flight — past
	// step 1's BeforeStep, with the steer watcher live — before sending, so
	// the steer lands mid-call instead of being drained at step 1's boundary.
	select {
	case <-comp.started:
	case <-time.After(5 * time.Second):
		t.Fatal("child never entered its first LLM call")
	}

	delivered := sendSteer(t, coord, handle, runID, taskID, "steer-urgent-1", steerUrgentBody, true)
	if !delivered {
		t.Fatal("urgent interrupt steer must be delivered to a running child")
	}

	// Bounded window: the interrupt must break the blocked call and the child
	// must complete with a non-error result.
	joinCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := coord.Join(joinCtx, handle)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results=%+v, want exactly 1", result.Results)
	}
	r := result.Results[0]
	if r.Err != nil {
		t.Fatalf("child result error: %v", r.Err)
	}
	if r.Status != "completed" {
		t.Fatalf("child status=%q, want completed (output=%s)", r.Status, r.Output)
	}
	if !strings.Contains(string(r.Output), steerSeenMarker) {
		t.Fatalf("child final result does not reflect the steer: %s", r.Output)
	}

	comp.mu.Lock()
	defer comp.mu.Unlock()
	if len(comp.canceled) != 1 || comp.canceled[0] != 0 {
		t.Fatalf("canceled calls=%v, want [0] (the interrupt must cancel only the first LLM call)", comp.canceled)
	}
	if !errors.Is(comp.firstErr, context.Canceled) {
		t.Fatalf("first call ctx error = %v, want context.Canceled", comp.firstErr)
	}
	if !comp.steerSeen {
		t.Fatal("second call did not see the steer body in its request history")
	}
	if len(comp.requests) < 2 {
		t.Fatalf("completer calls=%d, want >=2 (loop must soft-continue after the interrupt)", len(comp.requests))
	}
}

// TestSteerLandsAtStepBoundaryUnchanged (plan 53.03 regression): a non-urgent
// steer (Interrupt=false) to a child whose first call is gate-blocked on a
// test-controlled release channel must never interrupt the call. The steer
// waits; releasing the gate lets the first call complete, and the child's next
// call receives the steer body at the step boundary and completes.
func TestSteerLandsAtStepBoundaryUnchanged(t *testing.T) {
	comp := &steerInterruptCompleter{
		mode:       blockOnGate,
		started:    make(chan struct{}, 4),
		gated:      make(chan struct{}, 1),
		gate:       make(chan struct{}),
		expectBody: steerBoundaryBody,
	}
	coord, handle, runID, taskID := spawnSteerInterruptChild(t, comp)

	// Handshake: consume call 1's started signal, then wait until it is
	// provably blocked on the release channel (past step 1's BeforeStep,
	// watcher live). Both must be consumed so the grace-period select below
	// cannot read a stale signal from the first call.
	select {
	case <-comp.started:
	case <-time.After(5 * time.Second):
		t.Fatal("child never entered its first LLM call")
	}
	select {
	case <-comp.gated:
	case <-time.After(5 * time.Second):
		t.Fatal("child never blocked in its first LLM call")
	}

	delivered := sendSteer(t, coord, handle, runID, taskID, "steer-boundary-1", steerBoundaryBody, false)
	if !delivered {
		t.Fatal("boundary steer must be delivered to a running child")
	}

	// The steer must WAIT: hold the first call open for a bounded window so any
	// broken cancel path has a chance to fire. A second call starting while the
	// first is still gate-blocked means the steer interrupted the call — the
	// regression this test pins.
	select {
	case <-comp.started:
		t.Fatal("second LLM call began while the first was still gate-blocked; non-urgent steer interrupted the call")
	case <-time.After(steerBoundaryGrace):
	}
	close(comp.gate)

	joinCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := coord.Join(joinCtx, handle)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results=%+v, want exactly 1", result.Results)
	}
	r := result.Results[0]
	if r.Err != nil {
		t.Fatalf("child result error: %v", r.Err)
	}
	if r.Status != "completed" {
		t.Fatalf("child status=%q, want completed (output=%s)", r.Status, r.Output)
	}
	if !strings.Contains(string(r.Output), steerSeenMarker) {
		t.Fatalf("child final result does not reflect the steer: %s", r.Output)
	}

	comp.mu.Lock()
	defer comp.mu.Unlock()
	if len(comp.canceled) != 0 {
		t.Fatalf("canceled calls=%v, want none (a non-urgent steer must never interrupt an LLM call)", comp.canceled)
	}
	if !comp.steerSeen {
		t.Fatal("next call after the gate did not see the steer body in its request history")
	}
	if len(comp.requests) < 2 {
		t.Fatalf("completer calls=%d, want >=2 (steer must drain at the next step boundary)", len(comp.requests))
	}
}
