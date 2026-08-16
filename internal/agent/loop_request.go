package agent

// requestStep and its helpers live apart from loop.go: loop.go holds the run
// loop, step orchestration, and history shaping, while this file holds the
// single provider call, its prompt-too-long compact-and-retry, and the
// soft-steer sentinel mapping (plan 54 §4.3).

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func (l *Loop) requestStep(ctx context.Context, req provider.Request, opts Options) (*provider.Response, error) {
	// Model-thinking progress applies only to the model call. Stop it before
	// processing tool calls so it cannot replace live tool-batch progress.
	heartbeat, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	// Capture the cadence before spawning to prevent a concurrent test override.
	go emitModelThinkingHeartbeatAt(heartbeat, opts, modelThinkingHeartbeatInterval)

	// Soft-interrupt scope (plan 54 §4.3): a steer cancels ONLY the LLM call.
	// llmCtx is cancelable by the watcher below; the turn ctx (and any tool
	// batch running on it) is never canceled by a steer. The deferred
	// llmCancel() closes llmCtx when this call returns.
	llmCtx, llmCancel := context.WithCancel(ctx)
	defer llmCancel()
	// The steer scope arms the per-step watcher with the cancel func of the
	// LLM call currently in flight: the first attempt's llmCancel here, and
	// the prompt-too-long retry's retryCancel re-armed below, so a single
	// watcher can cancel whichever call is live.
	scope := &steerScope{cancel: llmCancel}
	// The watcher is inert without an interrupt channel or a watchdog
	// interval: pending gates alone must not spawn it (PERF-1) - a pending
	// check is a gate, never a cancel source.
	var steerFired atomic.Bool
	if opts.InterruptCh != nil || opts.WatchdogInterval > 0 {
		// stepDone bounds the watcher's life to this requestStep: it must NOT
		// exit on llmCtx.Done() (the retry runs on a fresh context), so the
		// deferred close is the watcher's guaranteed exit besides ctx.Done().
		stepDone := make(chan struct{})
		defer close(stepDone)
		go l.steerWatcher(ctx, scope, opts, &steerFired, stepDone)
	}

	estimatedTokens, _ := provider.EstimatePromptCost(req.Messages, req.Tools, l.contextAccounting())
	if err := l.workLimits.reserveProvider(estimatedTokens, requestOutputReserve(req)); err != nil {
		return nil, err
	}
	resp, err := l.Completer.ChatTurn(llmCtx, req)
	heartbeatCancel()
	// Prompt-too-long recovery: compact and retry exactly once
	// (retryAfterPromptTooLong); a second rejection propagates unchanged.
	resp, err, retried, estimatedTokens := l.maybeRetryPromptTooLong(ctx, req, opts, scope, resp, err, estimatedTokens)
	// R2: a successful retry replaced l.Messages with the pruned history (plus
	// the compaction notice), so the preparation prepareStep recorded points at
	// the rejected, never-sent history. Committing it would fingerprint a
	// BaseDigest over bytes the checkpoint does not hold. Discard the stale
	// preparation and re-Prepare on what the retry actually sent; a re-Prepare
	// failure fails the turn honestly rather than committing a stale digest.
	if err == nil && retried {
		if err := l.refreshPreparationAfterRetry(ctx, req, opts); err != nil {
			return nil, err
		}
	}
	// Map a steer-canceled LLM call to the soft sentinel ONLY when this call's
	// own watcher canceled the in-flight call (first attempt or retry) and the
	// turn ctx is still alive; a genuine provider error or hard cancel
	// propagates unchanged so the sentinel never masks real failures.
	err = l.mapSteerInterrupt(err, &steerFired, ctx, estimatedTokens, req)
	if err == errSteerInterrupt {
		return nil, err
	}
	if err == nil {
		l.emitTurnUsage(ctx, opts, req, resp, estimatedTokens)
	}
	return resp, err
}

// maybeRetryPromptTooLong compacts history and retries the model call exactly
// once after the provider rejects the prompt as too long; a second rejection
// propagates unchanged (fail fast, no retry loop). It re-arms the steer scope
// on the retry's LIVE context (DC-8): a steer that arrives DURING the retry
// cancels retryCtx, steerFired is set, and the caller's sentinel mapping
// refunds the never-consumed reservation and soft-continues, draining the
// steer at the next BeforeStep. A steer that fired BEFORE the retry armed the
// scope canceled only the first attempt's context and does not doom the retry.
// Returns the retry's response/error, whether a retry ran, and the
// re-estimated prompt cost.
func (l *Loop) maybeRetryPromptTooLong(ctx context.Context, req provider.Request, opts Options, scope *steerScope, resp *provider.Response, err error, estimatedTokens int) (*provider.Response, error, bool, int) {
	if err == nil || opts.DisableProviderReplay || !errors.Is(err, provider.ErrPromptTooLong) || ctx.Err() != nil {
		return resp, err, false, estimatedTokens
	}
	retryCtx, retryCancel := context.WithCancel(ctx)
	defer retryCancel()
	scope.set(retryCancel)
	resp, estimatedTokens, err = l.retryAfterPromptTooLong(req, opts, retryCtx, estimatedTokens)
	return resp, err, true, estimatedTokens
}

// mapSteerInterrupt maps a steer-canceled LLM call to the errSteerInterrupt
// sentinel. It fires ONLY when this call's own watcher canceled the in-flight
// call - the first attempt's llmCtx or the prompt-too-long retry's retryCtx
// (the error is that call's cancel) - and the turn ctx is still alive. A
// genuine provider error (500/timeout) that merely coincides with a steer fire
// - or a hard turn-ctx cancel - propagates unchanged: the sentinel must never
// mask real failures. The soft steer canceled the call before completion, so
// the prompt+output reservation charged above was never consumed: refund
// exactly it, or the next step's outputCap sees an exhausted budget and aborts
// the run with "work limit exceeded: output tokens" - breaking the soft-steer
// soft-continue contract (plan 54 §4.3). Genuine provider errors and hard
// cancels keep today's consume-and-abort semantics (no refund), so a refund
// can never widen the budget.
func (l *Loop) mapSteerInterrupt(err error, steerFired *atomic.Bool, ctx context.Context, estimatedTokens int, req provider.Request) error {
	if err == nil || !steerFired.Load() || !errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return err
	}
	l.workLimits.refundProvider(estimatedTokens, requestOutputReserve(req))
	return errSteerInterrupt
}
