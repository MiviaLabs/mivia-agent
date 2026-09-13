// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (serve.go) is chunk 7's scheduling loop: Service.Serve ticks
// over every enabled scheduled automation, dispatching a fire through the
// same RunOnce chunk 6 already built the instant its deadline elapses,
// never earlier and never more than once per elapsed deadline (D6
// skip-not-catch-up). deadlineMap is the in-memory next-fire tracker the
// loop consults each tick; see its own doc comment for the fire-storm
// invariant (DL-1) it exists to prevent.
package automation

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// serveTickInterval is how often Serve wakes to reconcile deadlines and
// dispatch due fires. It is a package-level var, not a const, precisely
// so a test can shrink it to make a schedule-driven test observe several
// ticks in a bounded wall-clock window instead of waiting out a real
// 30-second cadence.
var serveTickInterval = 30 * time.Second

// deadlineMap tracks each enabled scheduled automation's next-fire
// instant, keyed by automation ID. Invariant DL-1: no entry ever holds
// the zero time.Time{} value - Due()'s comparison is `!deadline.After(now)`,
// so a stored zero value would compare as "always due" and fire every
// tick forever (a fire-storm). Every write site below is guarded by an
// explicit IsZero() check before ever assigning into the map; there are
// exactly two such guarded write sites (refresh's never-seen branch, and
// advance) and no others.
type deadlineMap struct {
	deadlines map[string]time.Time
	// brokenLogged tracks which automation IDs currently have a broken
	// schedule that has already been logged, so refresh/advance log a
	// broken-schedule error once per TRANSITION into the broken state,
	// not on every tick a still-broken schedule is reconsidered. Cleared
	// the moment an automation is no longer broken (fixed, disabled, or
	// removed) so a later re-break logs again.
	brokenLogged map[string]bool
}

func newDeadlineMap() *deadlineMap {
	return &deadlineMap{
		deadlines:    make(map[string]time.Time),
		brokenLogged: make(map[string]bool),
	}
}

// noteBroken logs err once per transition into the broken state for id
// (see brokenLogged's doc comment above), unless err is ErrNoSchedule -
// a manual trigger has no schedule at all, which is not a fault and is
// never logged.
func (m *deadlineMap) noteBroken(id string, err error, logf func(format string, args ...any)) {
	if errors.Is(err, ErrNoSchedule) {
		return
	}
	if m.brokenLogged[id] {
		return
	}
	m.brokenLogged[id] = true
	if logf != nil {
		logf("automation %q: broken schedule: %v", id, err)
	}
}

// clearBroken forgets id's broken-schedule log state, so a later
// re-break (after a fix, a re-enable, or a re-add) logs again.
func (m *deadlineMap) clearBroken(id string) {
	delete(m.brokenLogged, id)
}

// refresh reconciles m against specs (every automation from LoadSpecs)
// at now. For an automation with an EXISTING entry that is still
// enabled: refresh does NOT touch it at all - recomputing an
// already-armed deadline is what caused the starvation bug this design
// replaces (an interval automation's deadline would be pushed forward
// every refresh and never actually elapse). Only advance() (below),
// called after an actual fire, may move an existing deadline forward.
// For an automation with NO existing entry (never seen, or re-seen
// after being pruned): compute NextFire(spec.Trigger, now) and handle
// its three-outcome contract:
//   - (t, nil), t non-zero  -> m.deadlines[id] = t
//   - (zero, nil)           -> exhausted one-shot (ScheduleAt fully
//     spent) - write NOTHING (the zero guard). Reconsidered fresh every
//     refresh call - cheap (one pure NextFire call) and self-healing if
//     the automation is later edited to a future time.
//   - (_, err)              -> broken schedule OR TriggerManual
//     (ErrNoSchedule) - write NOTHING. Log broken-schedule errors once
//     per transition into the broken state; do not log ErrNoSchedule at
//     all (manual triggers are not a fault).
//
// Automations no longer present in specs, or now disabled, are pruned
// (deleted) from the map entirely, so a later re-enable takes the
// never-seen path above and gets a fully fresh deadline with no stale
// carryover.
func (m *deadlineMap) refresh(specs []Spec, now time.Time, logf func(format string, args ...any)) {
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if !spec.Enabled {
			continue // pruned below, same as "not present at all"
		}
		seen[spec.ID] = true
		if _, ok := m.deadlines[spec.ID]; ok {
			continue // never touch an already-armed deadline
		}
		next, err := NextFire(spec.Trigger, now)
		if err != nil {
			m.noteBroken(spec.ID, err, logf)
			continue
		}
		m.clearBroken(spec.ID)
		if next.IsZero() {
			continue // exhausted one-shot: write nothing (DL-1 guard)
		}
		m.deadlines[spec.ID] = next
	}
	for id := range m.deadlines {
		if !seen[id] {
			delete(m.deadlines, id)
			m.clearBroken(id)
		}
	}
	for id := range m.brokenLogged {
		if !seen[id] {
			delete(m.brokenLogged, id)
		}
	}
}

// advance recomputes id's deadline after it fired at firedAt, using
// base := firedAt; if now.After(base) { base = now } - i.e.
// max(firedAt, now) - never a fresh `now` alone. This is what preserves
// interval phase when the daemon is keeping up, while a daemon that
// overslept several intervals still yields exactly ONE future fire
// (D6 skip-not-catch-up), never a backlog replay. Same three-outcome
// handling as refresh's never-seen branch: a (zero,nil) or error result
// deletes the entry outright (never assigns zero); a real time is
// written. These two guarded sites (refresh's never-seen branch, and
// this method) are the ONLY places that ever write to m.deadlines.
func (m *deadlineMap) advance(spec Spec, firedAt, now time.Time, logf func(format string, args ...any)) {
	base := firedAt
	if now.After(base) {
		base = now
	}
	next, err := NextFire(spec.Trigger, base)
	if err != nil {
		m.noteBroken(spec.ID, err, logf)
		delete(m.deadlines, spec.ID)
		return
	}
	m.clearBroken(spec.ID)
	if next.IsZero() {
		delete(m.deadlines, spec.ID) // exhausted one-shot (DL-1 guard)
		return
	}
	m.deadlines[spec.ID] = next
}

// due returns, sorted by automation ID, every ID whose stored deadline
// is <= now. Pure reader; never writes to the map.
func (m *deadlineMap) due(now time.Time) []string {
	var ids []string
	for id, deadline := range m.deadlines {
		if !deadline.After(now) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// nextWake returns the earliest stored deadline and true, or the zero
// value and false when the map is empty (caller then sleeps its own
// idle-poll tick interval, so a newly-enabled or freshly-edited
// automation is still picked up without a restart).
func (m *deadlineMap) nextWake() (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, deadline := range m.deadlines {
		if !found || deadline.Before(earliest) {
			earliest = deadline
			found = true
		}
	}
	return earliest, found
}

// Serve runs the automation scheduler until ctx is cancelled. Chunk 7
// deliberately does NOT call sweepInterrupted (claim.go's D13
// crash-recovery sweep) - that remains chunk 8/13's concern; a fresh
// Serve invocation starts scheduling immediately without a startup
// sweep pass.
//
// Each tick (default serveTickInterval): load enabled specs via
// LoadSpecs, m.refresh(specs, now, logf), then for each id in
// m.due(now), IN SEQUENCE - never concurrently, never as a goroutine
// fan-out (this sequential-only property is load-bearing; see
// cliautomations.HeadlessSpawner's doc comment for why):
//  1. run, err := s.RunOnce(ctx, id, ports.TriggerScheduled) (a RunOnce
//     error is logged and does NOT abort the tick or the loop - one
//     broken automation must not wedge every other one; a lost D7 claim
//     - RunSkipped run, nil error - is logged and treated as a normal,
//     non-fault outcome)
//  2. if closer, ok := s.spawn.(interface{ CloseLastRun() error }); ok {
//     _ = closer.CloseLastRun()
//     } (called UNCONDITIONALLY whether RunOnce succeeded or failed;
//     error from CloseLastRun is discarded - a cleanup failure must
//     never abort the scheduler; spawners that do not implement
//     CloseLastRun, e.g. the TUI's automationSessionSpawner, safely
//     skip this via the failed type assertion)
//  3. m.advance(spec, firedDeadline, now, logf) for that automation,
//     where firedDeadline is the deadline that WAS due (read before the
//     fire), never the wall clock at tick start.
//
// Returns ctx.Err() when ctx is cancelled.
func (s *Service) Serve(ctx context.Context) error {
	m := newDeadlineMap()
	logf := log.Printf
	for {
		s.serveTick(ctx, m, logf)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(serveTickInterval):
		}
	}
}

// serveTick runs exactly one scheduling pass: load specs, reconcile
// deadlines, and sequentially dispatch every due automation. Split out
// of Serve so a test can invoke individual ticks (indirectly, by
// controlling serveTickInterval) without needing to reimplement the
// dispatch loop.
func (s *Service) serveTick(ctx context.Context, m *deadlineMap, logf func(format string, args ...any)) {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		if logf != nil {
			logf("automation: serve: load specs: %v", err)
		}
		return
	}
	now := time.Now().UTC()
	m.refresh(specs, now, logf)

	specByID := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		specByID[spec.ID] = spec
	}

	for _, id := range m.due(now) {
		spec, ok := specByID[id]
		if !ok {
			continue
		}
		firedDeadline := m.deadlines[id]

		run, err := s.RunOnce(ctx, id, ports.TriggerScheduled)
		switch {
		case err != nil:
			if logf != nil {
				logf("automation %q: scheduled run failed: %v", id, err)
			}
		case run.State == ports.RunSkipped:
			// D7's documented lost-claim no-op: not a fault, but worth a
			// line so an operator can see a colliding fire happened.
			if logf != nil {
				logf("automation %q: scheduled fire skipped (lost fenced claim)", id)
			}
		}
		if closer, ok := s.spawn.(interface{ CloseLastRun() error }); ok {
			_ = closer.CloseLastRun()
		}

		m.advance(spec, firedDeadline, time.Now().UTC(), logf)
	}
}
