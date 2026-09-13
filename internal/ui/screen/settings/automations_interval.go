package settings

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// automations_interval provides a pure, UI-free model for the Automations settings
// editor's interval ("every") schedule value. It is strictly time-free (no time.Now
// anywhere) so the editor's status line cannot go stale between repaints.

const (
	defaultInterval       = time.Hour
	intervalRuleFormat    = "Every must be a duration like 30m or 2h"
	intervalRulePositive  = "Every must be positive"
	intervalRuleWholeSecs = "Every must be a whole number of seconds, at least 1s"
)

var intervalLadder = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
	time.Hour,
	3 * time.Hour,
	6 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
	48 * time.Hour,
	7 * 24 * time.Hour,
	14 * 24 * time.Hour,
	30 * 24 * time.Hour,
}

// parseInterval parses a human or machine duration string into a time.Duration.
// It tries time.ParseDuration first so every time.Duration.String() output round-trips;
// on failure it falls back to a lenient human form accepting day/week units and
// spelled-out units (e.g., 1d, 2 days, 30min, 30 seconds, 1h30, 1w).
// A bare number with no unit is refused, never guessed.
// Returns (d, "") on success or (0, ruleText) on failure.
// Locked rule texts (package constants intervalRuleFormat and intervalRulePositive are the single source):
//   - unparseable or blank -> intervalRuleFormat ("Every must be a duration like 30m or 2h")
//   - parsed but <= 0      -> intervalRulePositive ("Every must be positive")
func parseInterval(raw string) (time.Duration, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, intervalRuleFormat
	}
	if strings.Contains(trimmed, "+") {
		return 0, intervalRuleFormat
	}

	// Try Go canonical form first. This guarantees round-tripping for every
	// time.Duration.String() output (e.g. 1h0m30s, 24h0m0s, 500ms, 1ns).
	if d, err := time.ParseDuration(trimmed); err == nil {
		if d <= 0 {
			return 0, intervalRulePositive
		}
		return d, ""
	}

	// Lenient fallback for human-entered forms like "1d", "2 days", "30min",
	// "30 seconds", "1h30", "1w".
	d, err := parseHumanInterval(trimmed)
	if err != nil {
		return 0, intervalRuleFormat
	}
	if d <= 0 {
		return 0, intervalRulePositive
	}
	return d, ""
}

// parseHumanInterval tokenizes and sums human duration components.
func parseHumanInterval(s string) (time.Duration, error) {
	var total time.Duration
	var prevUnit string
	var hadAnyUnit bool
	i := 0

	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}

		if s[i] == '+' || s[i] == '-' {
			return 0, errors.New("unexpected sign in duration")
		}

		numStart := i
		hasDigits := false
		hasDot := false
		for i < len(s) {
			c := s[i]
			if c >= '0' && c <= '9' {
				hasDigits = true
				i++
			} else if c == '.' && !hasDot {
				hasDot = true
				i++
			} else {
				break
			}
		}
		if !hasDigits {
			return 0, errors.New("expected number")
		}

		val, err := strconv.ParseFloat(s[numStart:i], 64)
		if err != nil {
			return 0, err
		}

		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}

		unitStart := i
		for i < len(s) && ((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
			i++
		}
		unit := strings.ToLower(s[unitStart:i])

		unitDur, newPrevUnit, err := resolveUnit(s, i, unit, prevUnit)
		if err != nil {
			return 0, err
		}
		if unit != "" {
			hadAnyUnit = true
		}
		prevUnit = newPrevUnit

		nextTotal, err := addDurationComponent(total, val, unitDur)
		if err != nil {
			return 0, err
		}
		total = nextTotal
	}

	if !hadAnyUnit {
		return 0, errors.New("no unit found")
	}

	return total, nil
}

func resolveUnit(s string, i int, unit, prevUnit string) (time.Duration, string, error) {
	if unit != "" {
		d, ok := unitDuration(unit)
		if !ok {
			return 0, "", fmt.Errorf("unknown unit: %s", unit)
		}
		return d, unit, nil
	}

	// A bare number with no unit anywhere must be refused, never guessed:
	// assuming seconds vs minutes vs hours would silently execute on an unexpected
	// cadence with no operator consent. Only a trailing unit-less component following
	// a unit-bearing one ('1h30' -> 1h30m) adopts the previous unit's sub-unit.
	tail := i
	for tail < len(s) && (s[tail] == ' ' || s[tail] == '\t') {
		tail++
	}
	if tail != len(s) || prevUnit == "" {
		return 0, "", errors.New("bare number or non-trailing unitless component")
	}

	subDur, ok := subUnit(prevUnit)
	if !ok {
		return 0, "", fmt.Errorf("no sub-unit for %s", prevUnit)
	}
	return subDur, prevUnit, nil
}

func addDurationComponent(total time.Duration, val float64, unitDur time.Duration) (time.Duration, error) {
	prod := val * float64(unitDur)
	if math.IsNaN(prod) || math.IsInf(prod, 0) || prod < 0 || prod > float64(math.MaxInt64) {
		return 0, errors.New("interval overflow")
	}
	comp := time.Duration(prod)
	if comp < 0 || math.MaxInt64-total < comp {
		return 0, errors.New("interval overflow")
	}
	return total + comp, nil
}

func unitDuration(u string) (time.Duration, bool) {
	switch u {
	case "ns":
		return time.Nanosecond, true
	case "us", "µs":
		return time.Microsecond, true
	case "ms", "msec", "msecs", "millisecond", "milliseconds":
		return time.Millisecond, true
	case "s", "sec", "secs", "second", "seconds":
		return time.Second, true
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute, true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour, true
	case "d", "day", "days":
		return 24 * time.Hour, true
	case "w", "week", "weeks":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func subUnit(prev string) (time.Duration, bool) {
	switch prev {
	case "w", "week", "weeks":
		return 24 * time.Hour, true
	case "d", "day", "days":
		return time.Hour, true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Minute, true
	case "m", "min", "mins", "minute", "minutes":
		return time.Second, true
	case "s", "sec", "secs", "second", "seconds":
		return time.Millisecond, true
	default:
		return 0, false
	}
}

// formatInterval renders d into the shortest form that parseInterval accepts.
// Whole number of days -> "Nd"; otherwise h/m/s built from components with
// trailing zero units dropped and interior zeros kept (e.g. 1h0m30s stays 1h0m30s;
// 1h30m0s -> 1h30m; 24h0m0s -> 24h; 30m0s -> 30m; 90s -> 1m30s; 25h -> 25h).
// Sub-second or non-positive input falls back to d.String() unchanged rather than rounding.
// Note: naive strings.TrimSuffix of "0s"/"0m" is wrong because "1h0m30s" ends in
// the characters "0s" - build from components instead.
func formatInterval(d time.Duration) string {
	// Sub-second or non-positive input falls back to d.String() unchanged rather than rounding.
	if d <= 0 || d < time.Second || d%time.Second != 0 {
		return d.String()
	}

	// Whole number of days >= 2 days (48h) formats as "Nd".
	// 24h formats as "24h" to match Go/ladder conventions.
	if d >= 48*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}

	// Build h/m/s strictly from components. Naive strings.TrimSuffix of "0s" or "0m"
	// is a trap because valid formats like "1h0m30s" end with the character sequence "0s",
	// which suffix trimming would corrupt into "1h0m3".
	totalSec := int64(d / time.Second)
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60

	if h > 0 {
		switch {
		case m == 0 && s == 0:
			return fmt.Sprintf("%dh", h)
		case m > 0 && s == 0:
			return fmt.Sprintf("%dh%dm", h, m)
		case m == 0 && s > 0:
			return fmt.Sprintf("%dh0m%ds", h, s)
		default:
			return fmt.Sprintf("%dh%dm%ds", h, m, s)
		}
	}

	if m > 0 {
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}

	return fmt.Sprintf("%ds", s)
}

// stepInterval computes pure ladder math for stepper controls.
// If cur == 0 (blank field), it seeds defaultInterval.
// Otherwise it snaps cur to the nearest ladder rung - a tie rounds away from the
// start, i.e. toward the direction of travel (from 45m, delta +1 -> 1h, delta -1 -> 30m) -
// then moves delta rungs, clamped at both ends (never wraps).
func stepInterval(cur time.Duration, delta int) time.Duration {
	if cur == 0 {
		return defaultInterval
	}

	n := len(intervalLadder)
	if delta > n {
		delta = n
	} else if delta < -n {
		delta = -n
	}

	var target int

	switch {
	case cur <= intervalLadder[0]:
		if cur == intervalLadder[0] {
			target = delta
		} else if delta > 0 {
			target = delta - 1
		} else {
			target = 0
		}
	case cur >= intervalLadder[n-1]:
		if cur == intervalLadder[n-1] {
			target = (n - 1) + delta
		} else if delta < 0 {
			target = (n - 1) + (delta + 1)
		} else {
			target = n - 1
		}
	default:
		// cur sits between intervalLadder[0] and intervalLadder[n-1].
		for i := 0; i < n-1; i++ {
			if cur == intervalLadder[i] {
				target = i + delta
				break
			}
			if cur > intervalLadder[i] && cur < intervalLadder[i+1] {
				switch {
				case delta > 0:
					target = (i + 1) + (delta - 1)
				case delta < 0:
					target = i + (delta + 1)
				default:
					// delta == 0 snaps to nearest rung; ties round up.
					if cur-intervalLadder[i] < intervalLadder[i+1]-cur {
						target = i
					} else {
						target = i + 1
					}
				}
				break
			}
		}
	}

	if target < 0 {
		target = 0
	} else if target >= n {
		target = n - 1
	}
	return intervalLadder[target]
}

// validateInterval returns "" when d is legal for the on-disk wire format,
// else intervalRuleWholeSecs ("Every must be a whole number of seconds, at least 1s").
// The package constants are the single source of locked rule texts.
// The wire format is whole seconds (internal/automation truncates via int64(Every/time.Second)),
// so a sub-second value would silently save as every_seconds=0 - an armed schedule that can never fire.
// Deliberately has no upper bound here: internal/automation owns the ceiling
// (config.MaxTimeoutSeconds, enforced in store.go's validateSpec) and this package
// cannot import internal/config under .mivia/policy/import-layers.json, so a local copy
// would be free to drift. A too-large value is refused by the store at Apply and surfaced to the operator.
func validateInterval(d time.Duration) string {
	// Deliberately no upper bound here: internal/automation owns the ceiling (config.MaxTimeoutSeconds)
	// and this package cannot import internal/config under .mivia/policy/import-layers.json.
	if d < time.Second || d.Truncate(time.Second) != d {
		return intervalRuleWholeSecs
	}
	return ""
}

// intervalStatus returns the live one-line hint under the Every field.
// It is a pure function of the field's current text (no clock).
// Returns ok=true plus a subtle guidance line naming the formatted value and accepted units
// when the text parses and validates; ok=false plus the rule text otherwise.
func intervalStatus(raw string) (text string, ok bool) {
	d, rule := parseInterval(raw)
	if rule != "" {
		return rule, false
	}
	if rule := validateInterval(d); rule != "" {
		return rule, false
	}
	return fmt.Sprintf("Runs every %s (units: s, m, h, d, w)", formatInterval(d)), true
}
