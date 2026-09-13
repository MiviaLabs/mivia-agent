package automation

import (
	"errors"
	"testing"
	"time"
)

// TestNextFireManualTriggerReturnsErrNoSchedule covers the
// non-scheduled trigger path.
func TestNextFireManualTriggerReturnsErrNoSchedule(t *testing.T) {
	trig := TriggerSpec{Kind: TriggerManual}
	_, err := NextFire(trig, time.Now())
	if !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("NextFire(manual): err = %v, want ErrNoSchedule", err)
	}
}

// TestNextFireScheduledWithNilScheduleReturnsErrNoSchedule covers the
// defensive nil-Schedule guard (a TriggerScheduled trigger with no
// Schedule set, which ValidateSpec should reject upstream but NextFire
// must not silently misinterpret).
func TestNextFireScheduledWithNilScheduleReturnsErrNoSchedule(t *testing.T) {
	trig := TriggerSpec{Kind: TriggerScheduled, Schedule: nil}
	_, err := NextFire(trig, time.Now())
	if !errors.Is(err, ErrNoSchedule) {
		t.Fatalf("NextFire(scheduled, nil schedule): err = %v, want ErrNoSchedule", err)
	}
}

// TestNextFireIntervalSkipNotCatchUp is D6's central assertion: an
// interval automation whose `now` argument is passed far past several
// missed periods gets exactly ONE fire back - now + one interval - not
// a backlog and not a stampede. Calling NextFire repeatedly with an
// advancing `now` (the returned value becomes the next call's `now`)
// yields exactly one fire per call, forever, regardless of how far
// `now` jumped since the last real call.
func TestNextFireIntervalSkipNotCatchUp(t *testing.T) {
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind:         ScheduleInterval,
			EverySeconds: 60,
		},
	}
	// Simulate: the automation's last persisted next_fire_at was hours
	// ago, and 40+ periods have elapsed since. NextFire must still
	// return exactly one fire - now+60s - not a list, not 40 calls'
	// worth of catch-up.
	farPastFire := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := farPastFire.Add(40 * time.Hour) // ~2400 missed 60s periods
	next, err := NextFire(trig, now)
	if err != nil {
		t.Fatalf("NextFire: %v", err)
	}
	want := now.Add(60 * time.Second)
	if !next.Equal(want) {
		t.Fatalf("NextFire = %v, want %v (exactly one interval past `now`, not a catch-up walk from the stale fire)", next, want)
	}
	// Repeated calls each return exactly one fire, advancing by exactly
	// one interval - never a backlog.
	cur := now
	for i := 0; i < 5; i++ {
		n, err := NextFire(trig, cur)
		if err != nil {
			t.Fatalf("NextFire iteration %d: %v", i, err)
		}
		if !n.Equal(cur.Add(60 * time.Second)) {
			t.Fatalf("NextFire iteration %d = %v, want exactly one interval past %v", i, n, cur)
		}
		cur = n
	}
}

// TestNextFireIntervalRejectsNonPositiveEverySeconds asserts a
// non-positive EverySeconds is a broken-schedule error, not a silent
// "no more fires" (mirroring the plan's requirement that a caller must
// distinguish "validly no more fires" from "this schedule is broken").
func TestNextFireIntervalRejectsNonPositiveEverySeconds(t *testing.T) {
	for _, every := range []int64{0, -5} {
		trig := TriggerSpec{
			Kind:     TriggerScheduled,
			Schedule: &ScheduleSpec{Kind: ScheduleInterval, EverySeconds: every},
		}
		_, err := NextFire(trig, time.Now())
		if err == nil {
			t.Fatalf("NextFire(every_seconds=%d): got nil error, want rejection", every)
		}
	}
}

// TestNextFireIntervalRejectsOverflowingEverySeconds covers DC-7: huge
// EverySeconds values (e.g. >= 9,223,372,037) must not wrap negative to
// return a past time (which would trigger permanent fire storms). NextFire
// must yield either a validation error or a saturated future time, never a past time.
func TestNextFireIntervalRejectsOverflowingEverySeconds(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for _, every := range []int64{9223372037, 10000000000} {
		trig := TriggerSpec{
			Kind:     TriggerScheduled,
			Schedule: &ScheduleSpec{Kind: ScheduleInterval, EverySeconds: every},
		}
		next, err := NextFire(trig, now)
		if err != nil {
			continue
		}
		if !next.After(now) {
			t.Fatalf("NextFire(every_seconds=%d) = %v, want a saturated future time after %v (never a past time)", every, next, now)
		}
	}
}

// TestNextFireAtTimesOneRemaining asserts an at-times schedule with
// exactly one entry still in the future returns exactly that time.
func TestNextFireAtTimesOneRemaining(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind:    ScheduleAt,
			AtTimes: []string{past, future},
		},
	}
	next, err := NextFire(trig, now)
	if err != nil {
		t.Fatalf("NextFire: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, future)
	if !next.Equal(want) {
		t.Fatalf("NextFire = %v, want %v (the sole remaining future entry)", next, want)
	}
}

// TestNextFireAtTimesExhaustedReturnsZeroNotError asserts an at-times
// schedule with every entry in the past returns a zero time.Time with
// a nil error - distinguishable from a broken-schedule error, per D6/
// D10's "validly no more fires" vs "this schedule is broken"
// distinction, mirroring the SDK's own scheduler.At/atSchedule
// precedent.
func TestNextFireAtTimesExhaustedReturnsZeroNotError(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind: ScheduleAt,
			AtTimes: []string{
				now.Add(-2 * time.Hour).Format(time.RFC3339),
				now.Add(-time.Hour).Format(time.RFC3339),
			},
		},
	}
	next, err := NextFire(trig, now)
	if err != nil {
		t.Fatalf("NextFire: got error %v, want nil (exhausted at-times is not a broken schedule)", err)
	}
	if !next.IsZero() {
		t.Fatalf("NextFire = %v, want the zero time.Time for an exhausted at-times schedule", next)
	}
}

// TestNextFireAtTimesRejectsUnparseableEntry asserts a malformed
// RFC3339 entry is a broken-schedule error, not silently skipped or
// treated as exhausted.
func TestNextFireAtTimesRejectsUnparseableEntry(t *testing.T) {
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind:    ScheduleAt,
			AtTimes: []string{"not-a-timestamp"},
		},
	}
	_, err := NextFire(trig, time.Now())
	if err == nil {
		t.Fatal("NextFire: got nil error for an unparseable at-times entry, want rejection")
	}
}

// TestNextFireRecurringDelegatesToCronschedule asserts a
// ScheduleRecurring trigger delegates to cronschedule.Parse(...).Next
// correctly (same next-fire the underlying library would report
// directly).
func TestNextFireRecurringDelegatesToCronschedule(t *testing.T) {
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind: ScheduleRecurring,
			Cron: "0 0 * * *",
			TZ:   "UTC",
		},
	}
	now := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	next, err := NextFire(trig, now)
	if err != nil {
		t.Fatalf("NextFire: %v", err)
	}
	want := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextFire = %v, want %v", next, want)
	}
}

// TestNextFireRecurringSurfacesCronParseError asserts a broken cron
// expression surfaces as a real error - not a zero time.Time, which
// would be indistinguishable from a validly-exhausted at-times
// schedule.
func TestNextFireRecurringSurfacesCronParseError(t *testing.T) {
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind: ScheduleRecurring,
			Cron: "not a cron expr",
			TZ:   "UTC",
		},
	}
	next, err := NextFire(trig, time.Now())
	if err == nil {
		t.Fatalf("NextFire: got nil error for a broken cron expression, want rejection (got next=%v)", next)
	}
	if !next.IsZero() {
		t.Fatalf("NextFire: got non-zero next=%v alongside an error, want the zero time.Time on error", next)
	}
}

// TestNextFireRecurringSurfacesInvalidTZError asserts a broken TZ
// surfaces as a real error too.
func TestNextFireRecurringSurfacesInvalidTZError(t *testing.T) {
	trig := TriggerSpec{
		Kind: TriggerScheduled,
		Schedule: &ScheduleSpec{
			Kind: ScheduleRecurring,
			Cron: "0 0 * * *",
			TZ:   "Not/A_Real_Zone",
		},
	}
	_, err := NextFire(trig, time.Now())
	if err == nil {
		t.Fatal("NextFire: got nil error for an invalid TZ, want rejection")
	}
}

// TestNextFireUnknownScheduleKindErrors covers the default-case guard
// for a ScheduleKind value outside the three declared ones (e.g. a
// stale/corrupt on-disk value).
func TestNextFireUnknownScheduleKindErrors(t *testing.T) {
	trig := TriggerSpec{
		Kind:     TriggerScheduled,
		Schedule: &ScheduleSpec{Kind: ScheduleKind(99)},
	}
	_, err := NextFire(trig, time.Now())
	if err == nil {
		t.Fatal("NextFire: got nil error for an unknown schedule kind, want rejection")
	}
}
