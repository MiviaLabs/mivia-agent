// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (service_run.go) holds the Service's asynchronous run
// machinery: background execution started by Apply, the Watch
// subscription that streams run state to the UI, the fenced-claim
// heartbeat, and Close, which cancels every in-flight run.
package automation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// ErrRunNotActive is returned by CancelRun when runID names no run this
// Service is currently executing.
var ErrRunNotActive = fmt.Errorf("automation: run not active")

// ErrServiceClosed is returned when a run is started after Close.
var ErrServiceClosed = fmt.Errorf("automation: service closed")

// activeRun is one background execution the Service owns. holder is
// kept so Close can release the fenced claim of a run that does not
// stop within its bound.
type activeRun struct {
	cancel       context.CancelCauseFunc
	holder       string
	automationID string
}

// runWatcher is one Watch subscription. The publisher only writes
// latest and pokes signal, so a slow reader never blocks a run. The
// pump goroutine forwards latest to out; when several states arrive
// before the reader catches up, only the newest is delivered, so the
// terminal state is never lost.
type runWatcher struct {
	svc          *Service
	automationID string
	mu           sync.Mutex
	latest       *ports.Run
	signal       chan struct{}
	out          chan ports.Run
	done         chan struct{}
}

func (w *runWatcher) Events() <-chan ports.Run { return w.out }

// Cancel is idempotent: the watcher is removed and done closed only
// when it was still registered.
func (w *runWatcher) Cancel() { w.svc.unwatch(w) }

// offer stores run as the newest state and pokes the pump without
// blocking.
func (w *runWatcher) offer(run ports.Run) {
	w.mu.Lock()
	w.latest = &run
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// take returns and clears the newest state.
func (w *runWatcher) take() (ports.Run, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.latest == nil {
		return ports.Run{}, false
	}
	run := *w.latest
	w.latest = nil
	return run, true
}

// pump forwards states to the reader until done or ctx ends. It closes
// out on exit so a ranging reader terminates.
func (w *runWatcher) pump(ctx context.Context) {
	defer close(w.out)
	for {
		select {
		case <-w.done:
			return
		case <-ctx.Done():
			w.svc.unwatch(w)
			return
		case <-w.signal:
		}
		run, ok := w.take()
		if !ok {
			continue
		}
		select {
		case w.out <- run:
		case <-w.done:
			return
		case <-ctx.Done():
			w.svc.unwatch(w)
			return
		}
	}
}

// Watch registers a subscription for automationID's run state. Every
// state write of every run of that automation is offered to the handle
// until Cancel is called, ctx ends, or the Service closes. After Close
// the handle is returned already closed and nothing is registered.
func (s *Service) Watch(ctx context.Context, automationID string) (ports.RunHandle, error) {
	w := &runWatcher{
		automationID: automationID,
		signal:       make(chan struct{}, 1),
		out:          make(chan ports.Run),
		done:         make(chan struct{}),
		svc:          s,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		close(w.out)
		close(w.done)
		return w, nil
	}
	s.watchers[automationID] = append(s.watchers[automationID], w)
	s.mu.Unlock()
	go w.pump(ctx)
	return w, nil
}

// unwatch removes w and closes its done channel, only if w is still
// registered.
func (s *Service) unwatch(w *runWatcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.watchers[w.automationID]
	for i, cand := range list {
		if cand != w {
			continue
		}
		list = append(list[:i], list[i+1:]...)
		if len(list) == 0 {
			delete(s.watchers, w.automationID)
		} else {
			s.watchers[w.automationID] = list
		}
		close(w.done)
		return
	}
}

// publishRun delivers run's current state to every watcher of its
// automation. It never blocks on a reader.
func (s *Service) publishRun(run Run) {
	s.mu.Lock()
	list := append([]*runWatcher(nil), s.watchers[run.AutomationID]...)
	s.mu.Unlock()
	if len(list) == 0 {
		return
	}
	view := runToPorts(run)
	for _, w := range list {
		w.offer(view)
	}
}

// startAsync executes adm on a Service-owned goroutine. The run ctx
// derives from baseCtx, never from the caller's ctx, so a caller that
// returns does not cancel the run. The run ctx carries a cancel cause
// so endFailedRun can tell CancelRun from a shutdown. After Close, the
// already-admitted row is settled by abandonAdmitted and its claim is
// released.
func (s *Service) startAsync(adm admitted, exec func(context.Context, admitted) (ports.Run, error)) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.abandonAdmitted(adm)
		return ErrServiceClosed
	}
	runCtx, cancel := context.WithCancelCause(s.baseCtx)
	s.running[adm.run.ID] = activeRun{cancel: cancel, holder: adm.holder, automationID: adm.run.AutomationID}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer s.finishRun(adm.run.ID)
		if _, err := exec(runCtx, adm); err != nil && !errors.Is(err, ErrRunFenced) {
			log.Printf("automation %q: run %q: %v", adm.run.AutomationID, adm.run.ID, err)
		}
	}()
	return nil
}

// abandonAdmitted settles an admitted run this Service will never
// execute, then releases its claim. A row that is not running (a
// resume admitted a RunInterrupted or RunFailed row) keeps its state
// and message: only the claim was new. A running trigger row has no
// session, so it cannot be resumed; it becomes RunFailed with a message
// that says the run never started. A running row with a session
// becomes RunInterrupted, which a later resume admits. Each write goes
// through the fence and is published only if it landed.
func (s *Service) abandonAdmitted(adm admitted) {
	ctx := context.Background()
	switch {
	case adm.run.State != RunRunning:
	case adm.run.SessionName == "":
		s.endRun(ctx, adm.run, RunFailed, adm.run.StepIndex, RunFailJobError, closedBeforeStartMessage)
	default:
		s.endRun(ctx, adm.run, RunInterrupted, adm.run.StepIndex, RunFailNone, closedInterruptMessage)
	}
	_ = s.db.ReleaseClaim(ctx, claimKey(adm.run.AutomationID), adm.holder)
}

// finishRun forgets runID and releases its context.
func (s *Service) finishRun(runID string) {
	s.mu.Lock()
	ar, ok := s.running[runID]
	delete(s.running, runID)
	s.mu.Unlock()
	if ok {
		ar.cancel(nil)
	}
}

// CancelRun cancels the in-flight run runID with cause errUserCancel.
// The run row becomes RunCancelled once its current step observes the
// cancellation; this is the only path that yields RunCancelled.
func (s *Service) CancelRun(runID string) error {
	s.mu.Lock()
	ar, ok := s.running[runID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %q", ErrRunNotActive, runID)
	}
	ar.cancel(errUserCancel)
	return nil
}

// Close cancels every in-flight run and waits for them, bounded by
// ctx. A run that observes the cancellation records RunInterrupted. A
// run still executing when ctx ends is marked RunInterrupted here. Its
// fenced claim is released, so a later resume can re-claim it. The
// leaked goroutine keeps running until its step returns. Its own
// terminal write still lands while nobody has resumed the run. Once a
// resume rotates the claim token, that write is fenced out.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancelAll()

	waited := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waited)
	}()
	var err error
	select {
	case <-waited:
	case <-ctx.Done():
		err = fmt.Errorf("automation: close: %w", ctx.Err())
		s.interruptStuckRuns(ctx)
	}
	s.closeAllWatchers()
	return err
}

// interruptStuckRuns marks every run still registered as RunInterrupted
// and releases its claim. Each id is removed from running first so the
// executing goroutine's own finishRun is a no-op. The write is
// conditional on the row still being running. A run that finished
// meanwhile keeps its terminal state, and nothing is published for it.
func (s *Service) interruptStuckRuns(ctx context.Context) {
	s.mu.Lock()
	stuck := make(map[string]activeRun, len(s.running))
	for id, ar := range s.running {
		stuck[id] = ar
		delete(s.running, id)
	}
	s.mu.Unlock()
	wctx := context.WithoutCancel(ctx)
	for id, ar := range stuck {
		endedAt := time.Now().UTC()
		changed, uerr := s.interruptRunningRun(wctx, id, endedAt, closedInterruptMessage)
		switch {
		case uerr != nil:
			log.Printf("automation: close: mark run %q interrupted: %v", id, uerr)
		case changed:
			if run, ok, gerr := s.getRun(wctx, id); gerr == nil && ok {
				s.publishRun(run)
			} else {
				log.Printf("automation: close: read run %q: ok=%v err=%v", id, ok, gerr)
			}
		}
		_ = s.db.ReleaseClaim(wctx, claimKey(ar.automationID), ar.holder)
	}
}

// closeAllWatchers ends every Watch subscription.
func (s *Service) closeAllWatchers() {
	s.mu.Lock()
	var all []*runWatcher
	for _, list := range s.watchers {
		all = append(all, list...)
	}
	s.mu.Unlock()
	for _, w := range all {
		s.unwatch(w)
	}
}

// claimRefreshInterval resolves the heartbeat period: the configured
// value, or one third of the sweep threshold.
func (s *Service) claimRefreshInterval() time.Duration {
	if s.cfg.ClaimRefreshInterval > 0 {
		return s.cfg.ClaimRefreshInterval
	}
	return defaultSweepMaxAge / 3
}

// startClaimRefresh runs a heartbeat that refreshes the run's fenced
// claim until the returned stop func is called. The heartbeat derives
// from baseCtx, not the run ctx, so a cancelled run keeps its claim
// until its terminal write. A refresh error is logged, never fatal.
func (s *Service) startClaimRefresh(automationID, holder string) (stop func()) {
	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.claimRefreshInterval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.db.RefreshClaimFenced(ctx, claimKey(automationID), holder); err != nil && ctx.Err() == nil {
					log.Printf("automation %q: refresh claim: %v", automationID, err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// registerRunSession records that runID executes on the session with
// sessionID, so RunActiveForSession can answer whether a run owns a
// session the UI could send on. An empty sessionID (a run whose spawn
// had no context store still owns a conv with an id, so this is
// defensive only) registers nothing.
func (s *Service) registerRunSession(runID, sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	if s.runSessions == nil {
		s.runSessions = make(map[string]string)
	}
	s.runSessions[sessionID] = runID
	s.mu.Unlock()
}

// clearRunSession drops every session registration for runID. Called
// from the executing wrappers' defers, so every exit - success, any
// terminal failure, a fenced-out write, a panic - releases the session.
func (s *Service) clearRunSession(runID string) {
	s.mu.Lock()
	for sessionID, id := range s.runSessions {
		if id == runID {
			delete(s.runSessions, sessionID)
		}
	}
	s.mu.Unlock()
}

// RunActiveForSession reports whether a run of this Service currently
// executes on the session with the given id. Unlike a turn-in-flight
// check, the answer is stable across a run's inter-step gaps - it is
// the predicate a UI gates user sends on when a session is watched.
func (s *Service) RunActiveForSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runSessions[sessionID]
	return ok
}
