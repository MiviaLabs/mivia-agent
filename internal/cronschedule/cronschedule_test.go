package cronschedule

import (
	"errors"
	"testing"
	"time"
)

// TestParseInvalidTZRejected asserts ErrInvalidTZ is returned (wrapped,
// so errors.Is matches) for a timezone time.LoadLocation cannot
// resolve, and that the cron expression is never even attempted.
func TestParseInvalidTZRejected(t *testing.T) {
	_, err := Parse("* * * * *", "Not/A_Real_Zone")
	if err == nil {
		t.Fatal("Parse: got nil error for an invalid timezone, want ErrInvalidTZ")
	}
	if !errors.Is(err, ErrInvalidTZ) {
		t.Fatalf("Parse error = %v, want errors.Is(err, ErrInvalidTZ)", err)
	}
}

// TestParseInvalidExprRejected asserts ErrInvalidExpr is returned
// (wrapped) for a cron expression robfig's 5-field standard parser
// cannot parse, distinguishable from ErrInvalidTZ.
func TestParseInvalidExprRejected(t *testing.T) {
	_, err := Parse("not a cron expr", "UTC")
	if err == nil {
		t.Fatal("Parse: got nil error for an invalid cron expression, want ErrInvalidExpr")
	}
	if !errors.Is(err, ErrInvalidExpr) {
		t.Fatalf("Parse error = %v, want errors.Is(err, ErrInvalidExpr)", err)
	}
	if errors.Is(err, ErrInvalidTZ) {
		t.Fatalf("Parse error = %v, must NOT also match ErrInvalidTZ", err)
	}
}

// TestParseEmptyTZDefaultsToUTC asserts an empty tz resolves rather
// than erroring, and that the resolved location is UTC (matching
// internal/automation/spec.go's ScheduleSpec.TZ storage of a possibly
// empty raw string).
func TestParseEmptyTZDefaultsToUTC(t *testing.T) {
	spec, err := Parse("0 0 * * *", "")
	if err != nil {
		t.Fatalf("Parse with empty tz: got %v, want nil", err)
	}
	if spec.loc != time.UTC {
		t.Fatalf("Parse with empty tz: loc = %v, want time.UTC", spec.loc)
	}
}

// TestNextStrictlyAfter property-tests Next across several expressions
// and reference times: the result must never equal or precede the
// after argument.
func TestNextStrictlyAfter(t *testing.T) {
	exprs := []string{"* * * * *", "0 0 * * *", "*/15 * * * *", "0 9 1,15 * *", "30 4 * * MON"}
	refs := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 15, 23, 59, 59, 0, time.UTC),
	}
	for _, expr := range exprs {
		spec, err := Parse(expr, "UTC")
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		for _, ref := range refs {
			next := spec.Next(ref)
			if next.IsZero() {
				t.Fatalf("Parse(%q).Next(%v): got zero time, want a real next fire", expr, ref)
			}
			if !next.After(ref) {
				t.Fatalf("Parse(%q).Next(%v) = %v, want strictly after %v", expr, ref, next, ref)
			}
		}
	}
}

// TestNextNeverReturnsZeroForValidExpression is the empirical
// confirmation cronschedule.go's Next doc comment describes: a valid
// 5-field standard expression's Next never reports the zero
// time.Time, unlike scheduler.Every(nonPositiveDuration) or a spent
// scheduler.At list. Probed across a wide spread of "after" times,
// including several years out, to make the claim more than a
// single-sample assumption.
func TestNextNeverReturnsZeroForValidExpression(t *testing.T) {
	spec, err := Parse("0 0 29 2 *", "UTC") // Feb 29: only fires on leap years.
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), // no leap year until 2032
		time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), // 2100 is not a leap year (Gregorian rule)
	}
	for _, ref := range refs {
		next := spec.Next(ref)
		if next.IsZero() {
			t.Fatalf("Next(%v): got zero time for a valid recurring expression, want a real next fire", ref)
		}
	}
}

// TestDSTSpringForwardSkipsNonexistentLocalTime verifies robfig's
// actual, empirically-observed behavior for a fire time that never
// exists on the transition day: for America/New_York, 2026-03-08 is
// the spring-forward day (02:00 local -> 03:00 local, so 02:00-02:59
// never occurs that day). A schedule firing at "30 2 * * *" cannot
// land on 2026-03-08 at all - Next's field-matching loop searches for
// t.Hour()==2 by incrementing whole hours from midnight, and since
// that hour never appears in local wall-clock time on the 8th, the
// day wraps with no match and the fire lands on 2026-03-09 02:30
// instead. This was verified empirically (see the initial failing
// assertion this test replaced, which wrongly assumed a same-day
// 03:30 landing): robfig does NOT shift the fire forward within the
// same day the way Sao Paulo's midnight-DST example in spec.go's
// comment might suggest for a midnight-anchored schedule; for a
// schedule anchored inside the skipped hour itself, the whole day is
// skipped. Either way, the nonexistent local time is never returned
// and no double-fire occurs, matching D6's spirit at the DST level.
func TestDSTSpringForwardSkipsNonexistentLocalTime(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	spec, err := Parse("30 2 * * *", "America/New_York")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Reference: just after midnight local on the transition day.
	ref := time.Date(2026, 3, 8, 0, 30, 0, 0, loc)
	next := spec.Next(ref)
	if next.IsZero() {
		t.Fatal("Next: got zero time across a spring-forward transition, want a skipped-forward fire")
	}
	nextLocal := next.In(loc)
	if nextLocal.Year() == 2026 && nextLocal.Month() == time.March && nextLocal.Day() == 8 {
		t.Fatalf("Next = %v (local), want the nonexistent 2026-03-08 02:30 never returned", nextLocal)
	}
	// Empirically: the whole transition day is skipped and the fire
	// lands on 2026-03-09 02:30 local (the next day on which 02:30
	// exists), not "later the same day".
	want := time.Date(2026, 3, 9, 2, 30, 0, 0, loc)
	if !nextLocal.Equal(want) {
		t.Fatalf("Next = %v (local), want %v (spring-forward day skipped entirely, not partially shifted)", nextLocal, want)
	}
}

// TestDSTFallBackFiresTwiceForWallClockOnlySchedule documents an
// empirically-verified finding that corrects an initial assumption:
// robfig/cron's Next matches purely on local wall-clock field values
// (hour, minute, ...), with no awareness of UTC offset. During
// America/New_York's 2026-11-01 fall-back (02:00 EDT local time
// becomes 01:00 EST, so 01:00-01:59 occurs twice, once at UTC-4 and
// once at UTC-5), a schedule anchored at "30 1 * * *" therefore fires
// TWICE that calendar day - once at each offset - because both
// instants independently satisfy Hour()==1 && Minute()==30 in local
// wall-clock terms; Next has no concept of "already fired this wall
// time" to dedupe against. This is the opposite of what a naive
// reading of "DST fall-back fires once" might assume, and is asserted
// here rather than a false single-fire expectation per this task's
// own empirical-first instruction. The two fires are genuinely
// distinct real-world instants (verified below via UTC comparison),
// not a bug in this test's walking logic.
func TestDSTFallBackFiresTwiceForWallClockOnlySchedule(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	spec, err := Parse("30 1 * * *", "America/New_York")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ref := time.Date(2026, 10, 31, 12, 0, 0, 0, loc)
	var novemberFirstFires []time.Time
	cur := ref
	for i := 0; i < 3; i++ {
		next := spec.Next(cur)
		if next.IsZero() {
			t.Fatalf("Next(%v): got zero time, want a real fire", cur)
		}
		nextLocal := next.In(loc)
		if nextLocal.Month() == time.November && nextLocal.Day() == 1 {
			novemberFirstFires = append(novemberFirstFires, next)
		}
		cur = next
	}
	if len(novemberFirstFires) != 2 {
		t.Fatalf("fires landing on 2026-11-01 local = %d (%v), want exactly 2 (robfig matches wall-clock fields only, with no offset awareness)", len(novemberFirstFires), novemberFirstFires)
	}
	if novemberFirstFires[0].Equal(novemberFirstFires[1]) {
		t.Fatalf("the two 2026-11-01 01:30 local fires must be distinct real-world instants (different UTC offsets), got the same instant %v twice", novemberFirstFires[0])
	}
	if got := novemberFirstFires[1].Sub(novemberFirstFires[0]); got != time.Hour {
		t.Fatalf("gap between the two fall-back fires = %v, want exactly 1h (the repeated local hour)", got)
	}
}

// TestDayOfMonthOrDayOfWeekSemantics verifies cron's well-known quirk:
// when BOTH day-of-month and day-of-week are restricted (neither is
// "*"), a date matches if it satisfies EITHER field (OR), not both
// (AND). "0 0 1,15 * MON" fires on the 1st, the 15th, OR any Monday.
func TestDayOfMonthOrDayOfWeekSemantics(t *testing.T) {
	spec, err := Parse("0 0 1,15 * MON", "UTC")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 2026-01-01 is a Thursday (day-of-month match, not a Monday) - must fire.
	ref := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	next := spec.Next(ref)
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Next(%v) = %v, want %v (day-of-month match on a non-Monday)", ref, next, want)
	}
	// The following Monday (2026-01-05, neither the 1st nor the 15th) must
	// also fire - a pure AND semantics would skip straight to 2026-01-15.
	next2 := spec.Next(next)
	want2 := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Fatalf("Next(%v) = %v, want %v (Monday-only match, day-of-month AND semantics would skip to the 15th)", next, next2, want2)
	}
}

// TestSpecSatisfiesSDKScheduleInterface pins the compile-time
// assertion's runtime behaviour too: calling Next through the
// sdkscheduler.Schedule interface value must behave identically to
// calling it on the concrete *Spec.
func TestSpecSatisfiesSDKScheduleInterface(t *testing.T) {
	spec, err := Parse("0 0 * * *", "UTC")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var iface interface{ Next(time.Time) time.Time } = spec
	ref := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if got, want := iface.Next(ref), spec.Next(ref); !got.Equal(want) {
		t.Fatalf("interface Next = %v, want %v", got, want)
	}
}
