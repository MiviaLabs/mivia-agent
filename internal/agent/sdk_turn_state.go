package agent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// pass1Map hands pass-1 resultParts from the dispatcher shim
// (innermost) to the turn shaping wrapper (outermost), keyed by tool
// call ID so parallel calls under MaxConcurrentTools > 1 do not race.
type pass1Map struct {
	mu    sync.Mutex
	parts map[string]resultParts
}

// store records the pass-1 parts for callID.
func (m *pass1Map) store(callID string, p resultParts) {
	if callID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parts == nil {
		m.parts = make(map[string]resultParts)
	}
	m.parts[callID] = p
}

// take returns the stored parts for callID when body is exactly the
// parts' model-visible output (hook context appended), clearing the
// record. A body rewritten by an intermediate shim (the ref-only
// notice) misses and leaves the caller on its default single-pass path.
//
// The miss path ALWAYS deletes the stored entry: the dispatcher shim
// already ran for this call, so the entry can never again serve a
// later shaping pass (no other wrapper will see the original body
// either). Leaving it orphaned grows the map monotonically across a
// long session and balloons memory under MaxConcurrentTools > 1.
func (m *pass1Map) take(callID string, body string) (resultParts, bool) {
	if callID == "" {
		return resultParts{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parts == nil {
		return resultParts{}, false
	}
	p, ok := m.parts[callID]
	if !ok {
		return resultParts{}, false
	}
	delete(m.parts, callID)
	if p.cappedBody == "" {
		return resultParts{}, false
	}
	if body != appendHookContext(p.cappedBody, p.hookContext) {
		return resultParts{}, false
	}
	return p, true
}

// purge removes any stored pass-1 entry for callID. Callers that
// observe a mismatch via take should follow up with purge so the
// entry does not leak; the take helper already deletes on miss, but
// purge is the public seam for callers that never called take at all
// (e.g. an aborted or skipped call). Safe on a missing key.
func (m *pass1Map) purge(callID string) {
	if callID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.parts, callID)
}

// sdkTurnState is the per-run state shared by the SDK path's
// completer wrapper and tool shims, and the SINGLE place bridge and
// adapter errors surface (they are recorded here, never returned
// ad hoc from inside per-call hooks that have no error channel).
// steps counts completed Completer calls (the SDK loop's iteration
// counter as the host observes it) so tool dispatches can stamp
// runtime.Request.Step and re-issue an identical read in a later
// iteration without the turn-scoped dedup suppressing it. pass1
// carries the newest pass-1 result parts from the dispatcher shim to
// the turn shaping wrapper so a budget degrade reports the ORIGINAL
// body's true total and pages the original bytes, exactly like the
// legacy shapeBatch chain. dispatcher and spool hold the CURRENT
// surface rotation values: the CLI Surface hook can swap either
// mid-run (agentloop_adapter.go's surface bridge), and the shims
// read them per call instead of the wrap-time Options copy. shape
// owns the turn-level shaping counter so a surface rotation that
// rebuilds the registry keeps charging ONE shared budget. bridgeErr
// records the first surface-bridge failure so the run can fail with
// it after RunSteerable returns (the SDK Surface hook has no error
// channel; a nil return keeps the prior surface).
type sdkTurnState struct {
	steps      atomic.Int64
	pass1      pass1Map
	dispatcher atomic.Pointer[runtime.Dispatcher]
	spool      atomic.Pointer[remainder.Spool]
	// streamTee holds the teeWriter installed as the SDK run's
	// StreamingWriter, so RunAgentLoopOnce can feed it to
	// recordInterruptedPartial when a canceled run leaves streamed
	// bytes that the SDK's hard-fail Result never carried.
	streamTee atomic.Pointer[teeWriter]
	// advertised holds the run's pinned advertised ToolSpec snapshot:
	// the request-0 seed from Options.AdvertisedToolSpecs, replaced by
	// each surface rotation's non-nil ToolSpecs (the legacy keep-rule:
	// nil keeps the prior snapshot). The completer reads it per Chat
	// call and REPLACES the wire request's registry-derived tools with
	// it, so deferred tools outside the registry reach the wire from
	// request 0. See sdk_advertised.go's applyAdvertisedTools for the
	// recovery-request safety note.
	advertised atomic.Pointer[[]provider.ToolSpec]
	shapeOnce  sync.Once
	shape      *turnShapeCounter
	errMu      sync.Mutex
	bridgeErr  error
	// Tool-event synthesis state (sdk_tool_events.go): outcomes keyed
	// by tool call ID, and the once-per-iteration stream-revoke
	// gate (streamRevoked, armed at the first tool call, reset by the
	// EventIterationStart bus subscription).
	toolMu        sync.Mutex
	toolOutcomes  map[string]*toolCallOutcome
	streamRevoked atomic.Bool
	// cancels holds the per-call CancelFunc for every tool call
	// currently in flight in this turn, keyed by call ID (the same
	// key Run derives via toolCallKeyFromContext). Both the admitted
	// path (dispatcherShim.Run, invoked directly from the registry)
	// and the deferred/unadmitted path (RunUnadmittedTool, which
	// funnels through the SAME Run method via its own throwaway shim
	// instance) register here, so an external cancel-by-ID request
	// reaches either path identically. Entries are removed on call
	// completion (deferred alongside the timeout cancel) so the map
	// never outlives the calls it tracks.
	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc
}

// registerCancel installs the CancelFunc for an in-flight call. A
// blank callID is a no-op (mirrors the existing callKey == ""
// guards elsewhere in this file for ID-less test fixtures).
func (s *sdkTurnState) registerCancel(callID string, cancel context.CancelFunc) {
	if s == nil || callID == "" || cancel == nil {
		return
	}
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.cancels == nil {
		s.cancels = make(map[string]context.CancelFunc)
	}
	s.cancels[callID] = cancel
}

// deregisterCancel removes the CancelFunc for callID without invoking
// it, for the normal completion path where the call already finished
// and the timeout cancel (deferred alongside this one) is the one
// that actually releases the context. Safe to call more than once or
// on a missing key.
func (s *sdkTurnState) deregisterCancel(callID string) {
	if s == nil || callID == "" {
		return
	}
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.cancels, callID)
}

// cancelCall invokes and removes the CancelFunc for callID, returning
// whether a matching in-flight call was found. This is the entry
// point an external cancel request (e.g. a keypress in the UI) calls.
// Safe to call more than once for the same callID: the second call
// finds nothing and returns false.
func (s *sdkTurnState) cancelCall(callID string) bool {
	if s == nil || callID == "" {
		return false
	}
	s.cancelMu.Lock()
	cancel, ok := s.cancels[callID]
	if ok {
		delete(s.cancels, callID)
	}
	s.cancelMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func newSDKTurnState() *sdkTurnState { return &sdkTurnState{} }

// seedSurface installs the run's initial dispatcher and spool. It
// runs once from the single sdkTurnState construction site
// (buildAgentLoopOptions) so every later read starts from the
// caller's Options values, not zero.
func (s *sdkTurnState) seedSurface(dispatcher *runtime.Dispatcher, spool *remainder.Spool) {
	s.rotateSurface(dispatcher, spool)
}

// rotateSurface installs a surface rotation's dispatcher and spool.
// Nil values keep the current one, mirroring the legacy Surface
// contract's zero-field-means-keep rule.
func (s *sdkTurnState) rotateSurface(dispatcher *runtime.Dispatcher, spool *remainder.Spool) {
	if dispatcher != nil {
		s.dispatcher.Store(dispatcher)
	}
	if spool != nil {
		s.spool.Store(spool)
	}
}

// currentDispatcher returns the live dispatcher, or nil when no
// rotation and no seed ever installed one.
func (s *sdkTurnState) currentDispatcher() *runtime.Dispatcher { return s.dispatcher.Load() }

// currentSpool returns the live remainder spool, or nil when no
// rotation and no seed ever installed one.
func (s *sdkTurnState) currentSpool() *remainder.Spool { return s.spool.Load() }

// setStreamTee installs the run's StreamingWriter tee. It runs once
// from the single sdkTurnState construction site
// (buildAgentLoopOptions); a run without a FinalWriter never installs
// one and currentStreamTee stays nil.
func (s *sdkTurnState) setStreamTee(t *teeWriter) { s.streamTee.Store(t) }

// currentStreamTee returns the run's StreamingWriter tee, or nil when
// the run streamed nowhere.
func (s *sdkTurnState) currentStreamTee() *teeWriter { return s.streamTee.Load() }

// setAdvertised installs the advertised ToolSpec snapshot. A nil
// argument keeps the prior snapshot, mirroring the legacy Surface
// contract's nil-means-keep rule; a non-nil slice (empty included)
// replaces it.
func (s *sdkTurnState) setAdvertised(specs []provider.ToolSpec) {
	if specs == nil {
		return
	}
	s.advertised.Store(&specs)
}

// currentAdvertised returns the live advertised snapshot, or nil when
// neither a seed nor a rotation installed one.
func (s *sdkTurnState) currentAdvertised() []provider.ToolSpec {
	if p := s.advertised.Load(); p != nil {
		return *p
	}
	return nil
}

// shapeCounter lazily builds the one turn-level shaping counter a
// run (and every surface rotation inside it) shares.
func (s *sdkTurnState) shapeCounter() *turnShapeCounter {
	s.shapeOnce.Do(func() { s.shape = newTurnShapeCounter() })
	return s.shape
}

// resetIterationShaping resets nextIndex at the top of each iteration.
// A new broadcast channel is installed so the previous one - which the
// now-completed waiters drained - does not leak as a permanently-open
// pipe. The new channel reads as zero (open) for fresh waiters in
// the new iteration. The close of the previous channel must NOT
// re-close an already-closed one: abortIterationShaping can race this
// path during teardown and has already closed `old`; closing twice
// panics. The aborted flag tells us which side owns the channel's
// closure; only close when we still own it.
func (s *sdkTurnState) resetIterationShaping() {
	if s.shape != nil {
		s.shape.mu.Lock()
		s.shape.nextIndex = 0
		s.shape.aborted = false
		old := s.shape.signal
		s.shape.signal = make(chan struct{})
		owned := !s.shape.closedByAbort
		s.shape.closedByAbort = false
		s.shape.mu.Unlock()
		if owned {
			close(old)
		}
	}
}

// abortIterationShaping wakes any waiters when a batch aborts.
func (s *sdkTurnState) abortIterationShaping() {
	if s.shape != nil {
		s.shape.abort()
	}
}

// recordBridgeError keeps the FIRST bridge error; later ones are
// dropped because the first is the cause the operator needs.
func (s *sdkTurnState) recordBridgeError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.bridgeErr == nil {
		s.bridgeErr = err
	}
}

// bridgeError returns the recorded bridge error, if any.
func (s *sdkTurnState) bridgeError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.bridgeErr
}

// currentStep is the 1-based iteration the in-flight tool batch
// belongs to: the completer bumps steps at the top of each Chat, so a
// batch spawned by iteration N's response sees N.
func (s *sdkTurnState) currentStep() int { return int(s.steps.Load()) }
