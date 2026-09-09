// Package automation - this file (schedule.go) is Chunk 4's next-fire
// computation: NextFire maps a TriggerSpec to the single next fire
// strictly after a reference "now", per D6's skip-not-catch-up
// contract (docs/design/automations.md). It does not wire into any
// scheduler loop - that is chunk 6 (executor) and chunk 7 (serve
// loop)'s job. It is deliberately a pure function of (trigger, now):
// it takes no persisted "last next_fire_at" parameter beyond `now`
// itself, because D6's contract is "recomputed strictly in the future
// from the persisted next_fire_at" - the caller is expected to pass
// the persisted next_fire_at (or the current wall time, whichever is
// later) as `now`, and NextFire's job is only to ensure a single call
// never returns anything but the one fire immediately following it.
package automation

import (
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cronschedule"
)

// ErrNoSchedule is returned by NextFire when the trigger is not
// TriggerScheduled (e.g. TriggerManual), which has no next-fire
// concept at all. Distinguished from a zero time.Time return (which
// means "validly no more fires" for ScheduleAt) and from a wrapped
// parse error (which means "this schedule is broken").
var ErrNoSchedule = errors.New("automation: trigger has no schedule")

// NextFire computes the single next fire time for trigger strictly
// after now, per D6's skip-not-catch-up contract: a caller repeatedly
// invoking NextFire with an advancing `now` (e.g. the previously
// returned next-fire, or the current wall clock, whichever is later)
// gets exactly one fire per call, never a backlog. It distinguishes
// three outcomes:
//
//   - a real time.Time strictly after now: the next fire.
//   - a zero time.Time with a nil error: validly no more fires ever
//     (only possible for ScheduleAt once every entry is exhausted -
//     see scheduler.atSchedule's precedent in the SDK, which this
//     mirrors).
//   - a non-nil error: the schedule itself is broken (bad cron
//     expression, bad TZ, or trigger.Kind != TriggerScheduled) and the
//     caller must not treat that as "no more fires".
func NextFire(trigger TriggerSpec, now time.Time) (time.Time, error) {
	if trigger.Kind != TriggerScheduled || trigger.Schedule == nil {
		return time.Time{}, ErrNoSchedule
	}
	sched := trigger.Schedule
	switch sched.Kind {
	case ScheduleRecurring:
		spec, err := cronschedule.Parse(sched.Cron, sched.TZ)
		if err != nil {
			return time.Time{}, fmt.Errorf("automation: recurring schedule: %w", err)
		}
		return spec.Next(now), nil
	case ScheduleInterval:
		if sched.EverySeconds <= 0 {
			return time.Time{}, fmt.Errorf("automation: interval schedule: every_seconds must be positive, got %d", sched.EverySeconds)
		}
		// Skip-not-catch-up at the computation level: this always adds
		// exactly one interval to `now`, never walks forward from a stale
		// persisted timestamp in a loop. A caller that keeps `now` at a
		// stale value across repeated calls would get the same single
		// fire back each time (still no backlog); the caller's job (later
		// chunks) is to advance `now` to at least the previous result
		// before calling again, matching D6's "recomputed strictly in the
		// future from the persisted next_fire_at".
		return now.Add(time.Duration(sched.EverySeconds) * time.Second), nil
	case ScheduleAt:
		var next time.Time
		found := false
		for _, raw := range sched.AtTimes {
			ts, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return time.Time{}, fmt.Errorf("automation: at-times schedule: parse %q: %w", raw, err)
			}
			if !ts.After(now) {
				continue
			}
			if !found || ts.Before(next) {
				next = ts
				found = true
			}
		}
		if !found {
			// A fully-spent one-shot list: a REAL "never again" case,
			// unlike the recurring/cron case above (D10/D6's distinction,
			// mirroring the SDK's own scheduler.At/neverSchedule
			// precedent).
			return time.Time{}, nil
		}
		return next, nil
	default:
		return time.Time{}, fmt.Errorf("automation: unknown schedule kind %d", int(sched.Kind))
	}
}
