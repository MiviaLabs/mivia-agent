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
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

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
var _ sdktools.ProfiledTool = (*turnShapeWrapper)(nil)

func (w *turnShapeWrapper) Name() string { return w.inner.Name() }

// ExecutionProfile forwards the inner tool's profile: the SDK's
// run-timeout resolver consults only the outermost registered value,
// so every shim layer forwards explicitly (see dispatcherShim).
func (w *turnShapeWrapper) ExecutionProfile() sdktools.ExecutionProfile {
	return sdktools.ExecutionProfileOf(w.inner)
}

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
	callIndex := -1
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		callIndex = tc.Index
	}
	// Counted in flight for the whole call, tool execution included: a later
	// index must wait while this one is genuinely still working, and must not
	// wait once it is gone. The paired leave also opens the gate past this
	// index on the early returns below.
	w.counter.enter()
	defer w.counter.leave(callIndex)
	body, err := w.inner.Run(ctx, in)
	if err != nil {
		return body, err
	}
	s, ok := body.Value.(string)
	if !ok {
		return body, nil
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
	shaped, state, degraded, previewUsed := shapeOne(parts, remaining, w.counter.previewReserve, w.env)
	if parts.hookContext != "" {
		shaped = appendHookContext(shaped, parts.hookContext)
	}
	charged, advanced := w.chargeAndAdvance(degraded, state, len(shaped), callIndex, previewUsed)
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

// orderingHoleGraceWindow is how long a would-be escapee keeps waiting for
// a not-yet-scheduled predecessor before trusting that the index hole is
// permanent. A scheduling gap - the predecessor's worker goroutine simply
// has not run yet - resolves in microseconds, so the window is generous by
// several orders of magnitude; a genuine hole (a skipped plan) costs one
// bounded pause instead of the whole turn.
const orderingHoleGraceWindow = 250 * time.Millisecond

// waitForOrderingSlot blocks until the call index slot is ready or
// the run is aborted / ctx cancels. Caller must hold counter.mu.
//
// Order shaping by call index (F4). The wait is a channel select so a
// context cancel (no Cond.Broadcast on the cancel path) wakes the
// waiter - sync.Cond.Wait has no context awareness and would strand
// the goroutine indefinitely.
//
// With a toolcallctx.BatchOrder on ctx (SDKs that publish the batch's
// dispatch ledger) the wait is EXACT: a dispatched, unsettled
// predecessor is either running or not yet scheduled - never a
// permanent hole - so the waiter needs no heuristic at all. Without
// one (older SDKs), the count-based escape with its grace window
// remains the fallback.
func (w *turnShapeWrapper) waitForOrderingSlot(ctx context.Context, callIndex int) {
	if callIndex <= 0 {
		return
	}
	if order, ok := toolcallctx.BatchOrderFromContext(ctx); ok {
		w.waitForDispatchedPredecessors(ctx, order, callIndex)
		return
	}
	// holeSince is when the current potential-hole episode began; zero
	// while predecessors are visibly in flight.
	var holeSince time.Time
	for w.counter.nextIndex < callIndex && ctx.Err() == nil && !w.counter.aborted {
		// Only a call that is in flight and not itself parked here can still
		// advance the gate. When this call is the last such one, the index it
		// waits for is EITHER a hole nothing will fill (a duplicate plan the
		// SDK never dispatched, or a call rejected before the registry saw
		// it) OR an earlier call whose worker goroutine has not been
		// scheduled yet - the counts alone cannot tell the two apart.
		// Escaping immediately on the second shape charged the shared budget
		// in scheduling order instead of index order, so identical batches
		// produced different kept-bytes splits (the F4 determinism flake).
		// A hole is trusted only after it has looked like one for a
		// continuous grace window.
		if w.counter.inFlight-w.counter.blocked <= 1 {
			now := time.Now()
			if holeSince.IsZero() {
				holeSince = now
			}
			if now.Sub(holeSince) >= orderingHoleGraceWindow {
				return
			}
			w.counter.blocked++
			ch := w.counter.signal
			w.counter.mu.Unlock()
			timer := time.NewTimer(orderingHoleGraceWindow - now.Sub(holeSince))
			select {
			case <-ch:
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
			w.counter.mu.Lock()
			w.counter.blocked--
			continue
		}
		holeSince = time.Time{}
		w.counter.blocked++
		ch := w.counter.signal
		w.counter.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		w.counter.mu.Lock()
		w.counter.blocked--
	}
}

// waitForDispatchedPredecessors is the exact ordering wait: it blocks until
// every dispatched index below callIndex has either shaped here (the
// counter advanced past it) or settled in the SDK's ledger (its tool
// returned, it was rejected before the tools layer, or the batch abandoned
// it on abort). No grace timer: the ledger's settle-exactly-once contract
// makes "unsettled" mean "still coming", so waiting cannot strand and
// escaping cannot reorder. Caller must hold counter.mu.
func (w *turnShapeWrapper) waitForDispatchedPredecessors(ctx context.Context, order *toolcallctx.BatchOrder, callIndex int) {
	outstanding := func() bool {
		for _, d := range order.Dispatched() {
			if d >= callIndex {
				break
			}
			if d >= w.counter.nextIndex && !order.Settled(d) {
				return true
			}
		}
		return false
	}
	for outstanding() && ctx.Err() == nil && !w.counter.aborted {
		w.counter.blocked++
		ch := w.counter.signal
		changed := order.Changed()
		w.counter.mu.Unlock()
		select {
		case <-ch:
		case <-changed:
		case <-ctx.Done():
		}
		w.counter.mu.Lock()
		w.counter.blocked--
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
func (w *turnShapeWrapper) chargeAndAdvance(degraded bool, state degradeState, shapedLen, callIndex, previewUsed int) (int, bool) {
	if degraded && !state.refOnly {
		w.counter.charged = w.budget
		w.counter.previewReserve -= previewUsed
		if w.counter.previewReserve < 0 {
			w.counter.previewReserve = 0
		}
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
	budget, active := batchShapingBudget(opts)
	if !active {
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
			// Not restoring t: an unwrapped tool escapes the turn's batch
			// budget entirely, which is a bound on the context window rather
			// than a nicety. Add only fails on a duplicate name just removed.
			_ = t
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

// wrapTurnShaping returns inner wrapped by the turn-shaping wrapper when a
// batch budget is active, and inner unchanged otherwise.
//
// It exists because turn shaping is applied by wrapping registry tools, and
// the deferred-tool path executes a tool that is deliberately absent from the
// registry - so a deferred result escaped the batch budget entirely while the
// identical admitted result was charged and degraded. The budget defaults to
// derived-positive in every shipped session, so that divergence was live.
//
// It mirrors applyTurnShaping's per-tool wrapping exactly; the budget
// resolution is shared through batchShapingBudget so the two cannot disagree
// about when shaping is active.
func wrapTurnShaping(inner sdktools.Tool, cliTool tools.Tool, opts Options, turn *sdkTurnState) sdktools.Tool {
	if inner == nil || turn == nil {
		return inner
	}
	budget, active := batchShapingBudget(opts)
	if !active {
		return inner
	}
	var ephemeral bool
	var cap int
	if cliTool != nil {
		_, ephemeral = cliTool.(tools.EphemeralResultTool)
		if bt, ok := cliTool.(resultBudgetTool); ok {
			cap = bt.ResultBudgetBytes()
		}
	}
	return &turnShapeWrapper{
		inner:     inner,
		budget:    budget,
		counter:   turn.shapeCounter(),
		env:       newShapeEnv(turn.currentSpool(), opts.SessionID),
		ephemeral: ephemeral,
		toolName:  inner.Name(),
		cap:       cap,
		turn:      turn,
		onDegrade: func(charged, budget int) {
			emitBatchShapingRow(opts, charged, budget)
		},
	}
}

// batchShapingBudget resolves the turn's batch budget and whether shaping is
// active at all. A positive value is a literal; a negative one asks for the
// budget derived from the context window; zero disables shaping.
func batchShapingBudget(opts Options) (int, bool) {
	budget := opts.BatchResultBudgetBytes
	switch {
	case budget > 0:
		return budget, true
	case budget < 0:
		budget = derivedBatchBudget(opts.MaxContextTokens)
		return budget, budget > 0
	default:
		return 0, false
	}
}
