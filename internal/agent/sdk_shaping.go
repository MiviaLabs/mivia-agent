// Package agent - turn-level result shaping for the SDK backend.
//
// The SDK's TurnResultBudget OMITS an over-budget result with a bare
// notice; the CLI's contract is the legacy batch shaper's three tiers
// (fit unchanged / re-cut with an honest notice / notice alone) and
// "no call may be failed by the budget". This wrapper carries that
// contract onto the SDK path host-side: every converted tool is
// wrapped once more (outermost), the turn's budget is charged through
// one shared counter, and each result is shaped with the legacy
// shapeOne tiers against the bytes remaining in the turn.
//
// The SDK runs tool calls sequentially within a turn, so charging in
// call order is equivalent to the legacy batch-level allocation: at
// most one result straddles the boundary and pays the degrade floor.
// The D8 per-batch status line has no sequential analogue and is
// omitted; each degrade still carries its own honest notice, and a
// content-free heartbeat row is emitted per degraded result.
//
// The wrapper runs OUTSIDE the ref-only shim, so a ref-only notice
// (already notice-sized) is charged as emitted. While this wrapper is
// active the adapter leaves Options.TurnResultBudget unset so the
// SDK's omission path never engages.
package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// turnShapeCounter is the per-Run shared budget. One instance is
// created per RunAgentLoopOnce call and referenced by every wrapped
// tool, so sequential calls see the bytes their siblings spent.
// Under parallel dispatch (MaxConcurrentTools > 1), an internal
// broadcast channel serializes shaping in call index order so shaping
// remains deterministic (F4).
//
// The signal is a channel that the broadcaster swaps on every wake
// and the abort path closes once: a waiter that re-enters the wait
// after waking from a normal advance re-selects on the new channel
// (the old one is now garbage); a waiter that re-enters after an
// abort/cancel observes the closed channel and exits. This replaces
// sync.Cond.Wait, which has no context awareness and would strand a
// goroutine indefinitely when ctx cancels mid-batch (no SDK
// iteration boundary reaches the host to broadcast it).
type turnShapeCounter struct {
	mu            sync.Mutex
	signal        chan struct{} // closed = abort; swapped = normal advance
	nextIndex     int
	charged       int
	aborted       bool
	closedByAbort bool // tracks who owns the close of the CURRENT signal channel
}

func newTurnShapeCounter() *turnShapeCounter {
	return &turnShapeCounter{signal: make(chan struct{})}
}

// broadcast swaps the signal channel. Old waiters see the closed
// channel and re-select; new waiters see the fresh channel.
func (c *turnShapeCounter) broadcast() {
	c.mu.Lock()
	if c.aborted {
		c.mu.Unlock()
		return
	}
	ch := c.signal
	close(ch)
	c.signal = make(chan struct{})
	c.mu.Unlock()
}

// abort closes the signal channel permanently. Subsequent waiters
// observe the closed channel and exit immediately. closedByAbort
// records that THIS abort owns the close, so a racing reset does not
// try to close the same channel twice.
func (c *turnShapeCounter) abort() {
	c.mu.Lock()
	if !c.aborted {
		c.aborted = true
		c.closedByAbort = true
		close(c.signal)
	}
	c.mu.Unlock()
}

// turnShapeWrapper shapes one tool's result against the turn budget.
type turnShapeWrapper struct {
	inner     sdktools.Tool
	budget    int
	counter   *turnShapeCounter
	env       shapeEnv
	ephemeral bool
	toolName  string
	turn      *sdkTurnState
	// cap is the CLI tool's declared ResultBudgetBytes (0 = uncapped);
	// the re-cut target never exceeds it (F3).
	cap int
	// onDegrade reports a degraded result content-free (counts and
	// byte totals only), mirroring emitBatchShaping's row.
	onDegrade func(charged, budget int)
}

var _ sdktools.Tool = (*turnShapeWrapper)(nil)
var _ sdktools.SchemaTool = (*turnShapeWrapper)(nil)

func (w *turnShapeWrapper) Name() string { return w.inner.Name() }

func (w *turnShapeWrapper) ParameterSchema() []byte {
	if st, ok := w.inner.(sdktools.SchemaTool); ok {
		return st.ParameterSchema()
	}
	return nil
}

func (w *turnShapeWrapper) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	if st, ok := w.inner.(sdktools.SchemaTool); ok {
		return st.DecodeArguments(raw)
	}
	return sdktools.InOut{Value: raw}, nil
}

func (w *turnShapeWrapper) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	callKey := toolCallKeyFromContext(ctx, w.toolName)
	body, err := w.inner.Run(ctx, in)
	if err != nil {
		return body, err
	}
	s, ok := body.Value.(string)
	if !ok {
		return body, nil
	}
	callIndex := -1
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		callIndex = tc.Index
	}
	w.counter.mu.Lock()
	w.waitForOrderingSlot(ctx, callIndex)
	remaining := w.budget - w.counter.charged
	if remaining < 0 {
		remaining = 0
	}
	parts := w.resolveShapeParts(callKey, s)
	if parts.effectiveCap == 0 {
		parts.effectiveCap = w.cap
	}
	shaped, state, degraded := shapeOne(parts, remaining, w.env)
	if parts.hookContext != "" {
		shaped = appendHookContext(shaped, parts.hookContext)
	}
	charged, advanced := w.chargeAndAdvance(degraded, state, len(shaped), callIndex)
	w.counter.mu.Unlock()
	if advanced {
		w.counter.broadcast()
	}
	if degraded && w.onDegrade != nil {
		w.onDegrade(charged, w.budget)
	}
	// The re-cut body is what the model sees; the recorded tool-event
	// outcome follows it so tool_end matches (sdk_tool_events.go).
	if w.turn != nil && callKey != "" {
		w.turn.overwriteToolOutcomeBody(callKey, shaped)
	}
	return sdktools.Out{Value: shaped}, nil
}

// waitForOrderingSlot blocks until the call index slot is ready or
// the run is aborted / ctx cancels. Caller must hold counter.mu.
//
// Order shaping by call index (F4). The wait is a channel select so a
// context cancel (no Cond.Broadcast on the cancel path) wakes the
// waiter - sync.Cond.Wait has no context awareness and would strand
// the goroutine indefinitely.
func (w *turnShapeWrapper) waitForOrderingSlot(ctx context.Context, callIndex int) {
	if callIndex <= 0 {
		return
	}
	for w.counter.nextIndex < callIndex && ctx.Err() == nil && !w.counter.aborted {
		ch := w.counter.signal
		w.counter.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		w.counter.mu.Lock()
	}
}

// resolveShapeParts returns the parts the turn-shape layer shapes
// against: the dispatcher's pass-1 record when it captured this
// call, or the executed body as its own original when no pass-1
// ran (or an intermediate shim rewrote the body). Caller must hold
// counter.mu.
func (w *turnShapeWrapper) resolveShapeParts(callKey, s string) resultParts {
	if w.turn != nil && callKey != "" {
		if parts, found := w.turn.pass1.take(callKey, s); found {
			return parts
		}
	}
	return resultParts{
		cappedBody: s,
		totalN:     len(s),
		ephemeral:  w.ephemeral,
		toolName:   w.toolName,
	}
}

// chargeAndAdvance updates the counter after shapeOne. A budget-tier
// degrade spends the rest of the turn's budget (shapeBatch's F6
// rule): no later result may claim the floor. Returns the charged
// total and whether the counter's nextIndex was advanced past this
// callIndex, so the caller can broadcast. Caller must hold counter.mu.
func (w *turnShapeWrapper) chargeAndAdvance(degraded bool, state degradeState, shapedLen, callIndex int) (int, bool) {
	if degraded && !state.refOnly {
		w.counter.charged = w.budget
	} else {
		w.counter.charged += shapedLen
	}
	charged := w.counter.charged
	advanced := false
	if callIndex >= 0 {
		if callIndex >= w.counter.nextIndex {
			w.counter.nextIndex = callIndex + 1
			advanced = true
		}
	}
	return charged, advanced
}

// perCallResultBudget reads the CLI tool's honest ResultBudgetBytes
// contract (0 = uncapped) so the re-cut target never exceeds what the
// tool declared (F3).
type resultBudgetTool interface {
	ResultBudgetBytes() int
}

// applyTurnShaping wraps every tool in the SDK registry with the
// turn-level shaping wrapper. Positive BatchResultBudgetBytes is
// literal; negative selects the legacy derived-from-context budget
// (shape_batch.go:505-517); zero leaves the registry inert. The
// SDK's own TurnResultBudget stays unset across all three branches so
// its omission path never runs.
func applyTurnShaping(sdkReg *sdktools.Registry, cliReg *tools.Registry, opts Options, turn *sdkTurnState) {
	if sdkReg == nil {
		return
	}
	budget := opts.BatchResultBudgetBytes
	switch {
	case budget > 0:
		// literal
	case budget < 0:
		budget = derivedBatchBudget(opts.MaxContextTokens)
		if budget <= 0 {
			return
		}
	default:
		return
	}
	// The counter lives on the turn state so a mid-run surface
	// rotation that rebuilds the registry keeps charging the SAME
	// turn-level budget instead of resetting it per step. The spool
	// likewise reads the turn state's live value so a rotation's
	// RemainderSpool swap reaches degrade re-cuts minted after it.
	counter := turn.shapeCounter()
	env := newShapeEnv(turn.currentSpool(), opts.SessionID)
	for _, t := range sdkReg.Tools() {
		name := t.Name()
		var ephemeral bool
		var cap int
		if cliTool, ok := cliReg.Get(name); ok {
			_, ephemeral = cliTool.(tools.EphemeralResultTool)
			if bt, ok := cliTool.(resultBudgetTool); ok {
				cap = bt.ResultBudgetBytes()
			}
		}
		wrapped := &turnShapeWrapper{
			inner:     t,
			budget:    budget,
			counter:   counter,
			env:       env,
			ephemeral: ephemeral,
			toolName:  name,
			cap:       cap,
			turn:      turn,
			onDegrade: func(charged, budget int) {
				emitBatchShapingRow(opts, charged, budget)
			},
		}
		sdkReg.Remove(name)
		if err := sdkReg.Add(wrapped); err != nil {
			_ = sdkReg.Add(t)
		}
	}
}

// emitBatchShapingRow reports one degraded result content-free, the
// same row shape emitBatchShaping prints per batch.
func emitBatchShapingRow(opts Options, charged, budget int) {
	emit(opts, Event{
		Kind: EventHeartbeat,
		Detail: fmt.Sprintf("tool batch budget: 1 of 1 results degraded · %d/%d bytes charged",
			charged, budget),
	})
}
