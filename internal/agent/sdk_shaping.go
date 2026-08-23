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
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// turnShapeCounter is the per-Run shared budget. One instance is
// created per RunAgentLoopOnce call and referenced by every wrapped
// tool, so sequential calls see the bytes their siblings spent.
type turnShapeCounter struct {
	mu      sync.Mutex
	charged int
}

// turnShapeWrapper shapes one tool's result against the turn budget.
type turnShapeWrapper struct {
	inner     sdktools.Tool
	budget    int
	counter   *turnShapeCounter
	env       shapeEnv
	ephemeral bool
	toolName  string
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
	body, err := w.inner.Run(ctx, in)
	if err != nil {
		return body, err
	}
	s, ok := body.Value.(string)
	if !ok {
		return body, nil
	}
	w.counter.mu.Lock()
	remaining := w.budget - w.counter.charged
	w.counter.mu.Unlock()
	if remaining < 0 {
		remaining = 0
	}
	// The SDK path has no pass-1 capping: the executed body IS the
	// original, so cappedBody/totalN agree and refA is empty.
	parts := resultParts{
		cappedBody: s,
		totalN:     len(s),
		ephemeral:  w.ephemeral,
		toolName:   w.toolName,
	}
	parts.effectiveCap = w.cap
	shaped, state, degraded := shapeOne(parts, remaining, w.env)
	w.counter.mu.Lock()
	if degraded && !state.refOnly {
		// A budget-tier degrade spends the rest of the turn's budget
		// (shapeBatch's F6 rule): no later result may claim the floor.
		w.counter.charged = w.budget
	} else {
		w.counter.charged += len(shaped)
	}
	charged := w.counter.charged
	w.counter.mu.Unlock()
	if degraded && w.onDegrade != nil {
		w.onDegrade(charged, w.budget)
	}
	return sdktools.Out{Value: shaped}, nil
}

// perCallResultBudget reads the CLI tool's honest ResultBudgetBytes
// contract (0 = uncapped) so the re-cut target never exceeds what the
// tool declared (F3).
type resultBudgetTool interface {
	ResultBudgetBytes() int
}

// applyTurnShaping wraps every tool in the SDK registry with the
// turn-level shaping wrapper when a positive BatchResultBudgetBytes is
// configured. The SDK's own TurnResultBudget stays unset so its
// omission path never runs. A non-positive budget leaves the registry
// unchanged.
func applyTurnShaping(sdkReg *sdktools.Registry, cliReg *tools.Registry, opts Options) {
	if sdkReg == nil || opts.BatchResultBudgetBytes <= 0 {
		return
	}
	counter := &turnShapeCounter{}
	env := newShapeEnv(opts.RemainderSpool, opts.SessionID)
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
			budget:    opts.BatchResultBudgetBytes,
			counter:   counter,
			env:       env,
			ephemeral: ephemeral,
			toolName:  name,
			cap:       cap,
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
