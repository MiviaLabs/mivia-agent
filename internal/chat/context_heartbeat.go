package chat

import (
	"context"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// defaultContextHeartbeatInterval is how often a live process renews its
// context session lease. It mirrors internal/workflows/ledger's documented
// convention for DefaultClaimLease (refresh every Lease/3, so a live holder
// never reads as stale and a dead one is detected after at most one lease):
// internal/storage's sessionLeaseTTL is 2 minutes, so a 40s cadence gives the
// same three-heartbeats-per-lease safety margin without this package
// importing internal/storage's unexported constant.
const defaultContextHeartbeatInterval = 40 * time.Second

// contextHeartbeatTickTimeout bounds one renewal call so a slow or wedged
// store cannot block shutdown (stop() joins the ticking goroutine via done).
const contextHeartbeatTickTimeout = 10 * time.Second

// contextHeartbeat owns a ticking goroutine that periodically renews the lease
// for a (store, principal) pair against rival ReclaimSession conditional takeovers.
// It is store-binding: arm() dedupes by store alone, and the ticker reads the
// current principal each tick via principalFn (Session.ContextPrincipal).
// RotateSessionID and reclaimContextSession update the principal without
// restarting the heartbeat; the next tick uses the new principal directly.
//
// Production lifetime: Session.ReleaseContextLease (in dispatchChatSurface exit)
// invokes release() so clean exits do not leave leases live for sessionLeaseTTL
// (2 minutes), preventing ErrSessionLiveElsewhere on quick resumes. release() is
// best-effort, never blocks shutdown beyond contextHeartbeatTickTimeout, and only
// covers primary sessions held by dispatchChatSurface (TUI SessionPool sessions
// without active focus release on process exit).
type contextHeartbeat struct {
	mu sync.Mutex
	// principalFn is the live-principal accessor, bound once at construction
	// (newContextHeartbeat) and never reassigned by arm(). Reading it happens
	// only from the ticking goroutine, never synchronously from arm() or
	// stop() - see arm()'s doc comment for why that matters.
	principalFn func() contextstate.Principal
	store       contextstate.Store
	principal   contextstate.Principal
	interval    time.Duration
	cancel      context.CancelFunc
	done        chan struct{}
}

// newContextHeartbeat constructs an unarmed heartbeat bound to principalFn,
// the accessor the ticking goroutine will call on every tick once armed.
// interval <= 0 falls back to defaultContextHeartbeatInterval.
func newContextHeartbeat(interval time.Duration, principalFn func() contextstate.Principal) *contextHeartbeat {
	if interval <= 0 {
		interval = defaultContextHeartbeatInterval
	}
	return &contextHeartbeat{interval: interval, principalFn: principalFn}
}

// arm starts (or, on a repeated call naming the same store, leaves running)
// the ticking goroutine that renews store's lease for principal's session.
//
// arm never calls currentPrincipal (principalFn) synchronously here - it only
// starts a goroutine, or leaves one running, and reads live principal state
// exclusively from within that goroutine's own tick. This is the load-bearing
// lock-safety property: both real call sites (Session.SetContextManager,
// Session.SetContextStore) call arm() AFTER releasing s.mu, but if arm()
// itself synchronously called something that re-takes s.mu (like
// s.ContextPrincipal(), which RLocks), and a caller somehow still held s.mu,
// it would deadlock. Structuring arm() to only ever store a closure/start a
// goroutine, never invoke the principal-reading closure inline, makes that
// structurally impossible.
//
// Dedup is store-scoped, not principal-scoped (contextHeartbeat is
// store-binding): a second arm() call naming the same store leaves the
// existing goroutine running untouched, because that goroutine already reads
// whatever principal is current via principalFn on its own next tick - no
// restart is needed to pick up a principal rotation. A different store
// replaces the running goroutine.
func (h *contextHeartbeat) arm(store contextstate.Store, principal contextstate.Principal) {
	if store == nil {
		return
	}
	h.mu.Lock()
	if h.cancel != nil && h.store == store {
		// Same store already armed: leave the running goroutine alone. Update
		// the recorded principal purely for introspection/tests; the ticking
		// goroutine never reads this field.
		h.principal = principal
		h.mu.Unlock()
		return
	}
	// Replacing a different store: capture the old goroutine's cancel so it
	// can be stopped without leaking, but do that outside the lock - canceling
	// is cheap and non-blocking, so this never turns arm() into a call that
	// could block while holding h.mu.
	oldCancel := h.cancel
	h.stopLocked()
	h.store = store
	h.principal = principal
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	done := make(chan struct{})
	h.done = done
	interval := h.interval
	h.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	go h.run(ctx, interval, done)
}

// stop cancels the ticking goroutine if one is running and waits for it to
// exit, so no renewal write can land after stop returns. No production
// caller in this iteration (see the type's doc comment); called only by
// tests.
func (h *contextHeartbeat) stop() {
	h.mu.Lock()
	cancel := h.cancel
	done := h.done
	h.stopLocked()
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// release stops the ticking goroutine and, best-effort, clears this
// process's lease so the NEXT resume of this session id sees an
// immediately-stale (NULL) lease instead of waiting out sessionLeaseTTL
// against a process that already quit cleanly. Safe to call on an unarmed
// heartbeat (no-op) or with a store that doesn't implement release support.
// This IS the production call site the type's package doc comment used to
// say did not exist - see Session.ReleaseContextLease, wired at the one
// choke point every chat surface (one-shot, REPL, TUI) returns through.
func (h *contextHeartbeat) release(ctx context.Context) {
	h.mu.Lock()
	store := h.store
	principalFn := h.principalFn
	h.mu.Unlock()
	h.stop()
	if store == nil || principalFn == nil {
		return
	}
	renewer, ok := store.(contextstate.SessionLeaseRenewer)
	if !ok {
		return
	}
	principal := principalFn()
	if !principal.IsBound() {
		return
	}
	releaseCtx, cancel := context.WithTimeout(ctx, contextHeartbeatTickTimeout)
	defer cancel()
	_ = renewer.ReleaseLease(releaseCtx, principal, principal.SessionID)
}

// stopLocked clears the armed state under h.mu. It does not itself cancel or
// wait: callers that need the cancel/wait side effects (stop, arm replacing a
// different store) capture cancel/done before or after calling this and act
// on them outside anything that must stay non-blocking under the lock.
func (h *contextHeartbeat) stopLocked() {
	h.cancel = nil
	h.done = nil
	h.store = nil
	h.principal = contextstate.Principal{}
}

// run is the ticking goroutine body. It exits (closing done) as soon as ctx
// is canceled, whether by arm() replacing this heartbeat's store or by stop().
func (h *contextHeartbeat) run(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.renew(ctx)
		}
	}
}

// renew renews the lease for the live principal, read fresh via principalFn
// on this tick - never the principal snapshot arm() was called with.
func (h *contextHeartbeat) renew(ctx context.Context) {
	h.mu.Lock()
	store := h.store
	principalFn := h.principalFn
	h.mu.Unlock()
	if store == nil || principalFn == nil {
		return
	}
	renewer, ok := store.(contextstate.SessionLeaseRenewer)
	if !ok {
		return
	}
	principal := principalFn()
	if !principal.IsBound() {
		return
	}
	tickCtx, cancel := context.WithTimeout(ctx, contextHeartbeatTickTimeout)
	defer cancel()
	_ = renewer.RenewLease(tickCtx, principal, principal.SessionID)
}

// armContextHeartbeat lazily constructs s.contextHeartbeat (bound once to
// s.ContextPrincipal as its live-principal accessor) and arms it for store.
// Callers MUST NOT hold s.mu: sync.Once.Do uses its own internal lock, not
// s.mu, and arm() itself never takes s.mu either (see arm's doc comment), so
// this is safe to call after releasing s.mu but would be redundant, not
// unsafe, to call before - the point is that neither path deadlocks.
func (s *Session) armContextHeartbeat(store contextstate.Store, principal contextstate.Principal) {
	s.contextHeartbeatOnce.Do(func() {
		s.contextHeartbeat = newContextHeartbeat(defaultContextHeartbeatInterval, s.ContextPrincipal)
	})
	s.contextHeartbeat.arm(store, principal)
}

// ReleaseContextLease stops this session's context-lease heartbeat, if one
// was ever armed, and clears its lease so the next resume of this session id
// does not have to wait out sessionLeaseTTL against a process that already
// quit cleanly. Safe to call on a session that never bound a context store
// (no-op). Callers must not hold s.mu - see contextHeartbeat.arm's doc
// comment for why release() (like arm()) must run lock-free.
func (s *Session) ReleaseContextLease(ctx context.Context) {
	if s.contextHeartbeat == nil {
		return
	}
	s.contextHeartbeat.release(ctx)
}

// StopContextLeaseHeartbeat stops this session's context-lease heartbeat
// WITHOUT touching the durable lease. Same goroutine-join contract as
// ReleaseContextLease (callers must not hold s.mu), minus the store write.
//
// This is for the one case where a session must be abandoned but its lease
// row must NOT be cleared: a session built to resume an id that Load then
// resolved to a session ALREADY live in this process. The built twin is
// thrown away, but release() reads the LIVE principal - which Load has
// already rewritten to the resolved id - so releasing it would clear the
// lease of the live session that still owns that row, leaving a conversation
// the user is actively using with a heartbeat that renews nothing and a row
// any other process may immediately reclaim.
func (s *Session) StopContextLeaseHeartbeat() {
	if s.contextHeartbeat == nil {
		return
	}
	s.contextHeartbeat.stop()
}
