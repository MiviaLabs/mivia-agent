package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

type toolExecResult struct {
	index    int // original position in ToolCalls slice
	toolCall provider.ToolCall
	// result is the model-visible body as pass 1 built it, hook context
	// included. It is what enters history when no batch budget is configured,
	// and what the operator-facing tool_end preview is drawn from.
	result    string
	truncated bool // whether result was truncated for history
	err       error
	// parts is the same result in structured form, which is what the batch
	// shaper needs: a second pass has to know the ORIGINAL body's size and
	// where its bytes live, and neither survives being flattened into one
	// string (D9). Every construction site must populate it - a result with
	// an empty parts.cappedBody would be charged as zero bytes and emitted as
	// an empty tool message.
	parts           resultParts
	ephemeralMarker string
	// hookRuns are the lifecycle hooks that fired for this call, for display.
	hookRuns []runtime.HookRun
}

// errorExecResult closes out a call that produced no tool body of its own.
// The synthesized text IS the model's result for that call, so it is charged
// against the batch budget like any other body (C7).
func errorExecResult(idx int, call provider.ToolCall, err error) toolExecResult {
	body := "error: " + err.Error()
	return toolExecResult{
		index: idx, toolCall: call, result: body, err: err,
		parts: resultParts{cappedBody: body, totalN: len(body)},
	}
}

// buildExecResult turns one dispatcher outcome into the loop's record of the
// call: the model-visible body pass 1 produced, and the structured parts a
// later batch-shaping pass needs (D9).
func buildExecResult(idx int, task *toolTask, reg *tools.Registry, opts Options, r runtime.Result) toolExecResult {
	result, err := string(r.Output), r.Err
	// Keep model-visible tool bodies; only synthesize an error when empty.
	if err != nil && strings.TrimSpace(result) == "" {
		result = fmt.Sprintf("error: %v", err)
	}
	// totalN is captured BEFORE the cap because it is the only surviving
	// record of how big the tool's answer really was: capping drops the
	// original, and a second shaping pass has to be able to tell the model the
	// true total rather than the size of what it happened to keep.
	totalN := len(result)
	// D10: the ephemeral status must be known BEFORE capping. Capping spools
	// the full body under ref:output:<digest> and the notice names that ref;
	// a ref outlives ScrubEphemeralToolMessages and would let the model page
	// back, via read_output, exactly the bytes the scrub exists to remove. So
	// an ephemeral body is capped with a NIL spool: CapWithSpoolRef then mints
	// no ref (plain notice), keeping the honest kept/total report while never
	// storing the body. The marker/ephemeral flags still ride along for the
	// batch shaper and the final scrub.
	marker, ephemeral := "", false
	if tool, ok := reg.Get(task.call.Function.Name); ok {
		if ephemeralTool, ok := tool.(tools.EphemeralResultTool); ok {
			marker, ephemeral = ephemeralTool.EphemeralResultMarker(task.raw), true
		}
	}
	effectiveCap := effectiveResultCap(opts.MaxToolResultChars, task.capability.MaxResultBytes)
	spool := opts.RemainderSpool
	if ephemeral {
		spool = nil
	}
	capped, refA, truncated := remainder.CapWithSpoolRef(spool, opts.SessionID, result, effectiveCap)
	// Hook context is attached AFTER the tool result was capped, and rides above
	// that cap within its own fixed bound (runtime.MaxHookContextBytes). Paying
	// for a formatter's advice out of the tool's own budget would destroy real
	// result bytes to make room for commentary about them. It travels in parts
	// as well, unattached, because shaping re-attaches it after the fact.
	return toolExecResult{
		index: idx, toolCall: task.call, result: appendHookContext(capped, r.HookContext),
		truncated: truncated, err: err, ephemeralMarker: marker, hookRuns: r.HookRuns,
		parts: resultParts{
			cappedBody: capped, refA: refA, totalN: totalN, effectiveCap: effectiveCap,
			hookContext: r.HookContext, truncated: truncated, ephemeral: ephemeral,
		},
	}
}

type toolTask struct {
	call       provider.ToolCall
	raw        json.RawMessage
	capability tools.Capability
	timeout    time.Duration
	callCtx    context.Context
	cancel     context.CancelFunc
}

type toolScheduler struct {
	limit chan struct{}
	mu    sync.Mutex
	locks map[string]*keyLock
}

// keyLock wraps a per-key mutex channel with a reference count so the
// scheduler can clean up entries that are no longer in use, preventing
// unbounded map growth over long sessions.
type keyLock struct {
	ch   chan struct{}
	refs int32
}

func newToolScheduler(limit int) *toolScheduler {
	if limit <= 0 {
		limit = 4
	}
	return &toolScheduler{limit: make(chan struct{}, limit), locks: make(map[string]*keyLock)}
}

func (s *toolScheduler) acquire(ctx context.Context, key string) (func(), error) {
	select {
	case s.limit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if key == "" {
		return func() { <-s.limit }, nil
	}
	s.mu.Lock()
	kl := s.locks[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		s.locks[key] = kl
	}
	kl.refs++
	s.mu.Unlock()
	select {
	case kl.ch <- struct{}{}:
		return func() {
			<-kl.ch
			s.mu.Lock()
			kl.refs--
			// Only clean up when this goroutine was the last reference
			// AND no one is waiting on the channel.
			if kl.refs <= 0 && len(kl.ch) == 0 {
				delete(s.locks, key)
			}
			s.mu.Unlock()
			<-s.limit
		}, nil
	case <-ctx.Done():
		s.mu.Lock()
		kl.refs--
		// A canceled waiter can be the last reference, so it owes the same
		// cleanup as the release path. Keys are per file path, so skipping it
		// grows the map without bound over a long session.
		if kl.refs <= 0 && len(kl.ch) == 0 {
			delete(s.locks, key)
		}
		s.mu.Unlock()
		<-s.limit
		return nil, ctx.Err()
	}
}
