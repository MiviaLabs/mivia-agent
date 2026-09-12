package settings

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseIntervalRoundTripsEveryLadderRung(t *testing.T) {
	for _, rung := range intervalLadder {
		t.Run(rung.String(), func(t *testing.T) {
			formatted := formatInterval(rung)
			got, rule := parseInterval(formatted)
			if rule != "" {
				t.Fatalf("parseInterval(%q) unexpected rule %q", formatted, rule)
			}
			if got != rung {
				t.Fatalf("parseInterval(%q) = %v, want %v", formatted, got, rung)
			}
		})
	}
}

func TestParseIntervalAcceptsGoCanonicalForms(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
	}{
		{name: "30m", d: 30 * time.Minute},
		{name: "1h30m", d: 1*time.Hour + 30*time.Minute},
		{name: "1h0m30s", d: 1*time.Hour + 30*time.Second},
		{name: "24h", d: 24 * time.Hour},
		{name: "large", d: 2562047*time.Hour + 47*time.Minute + 16*time.Second},
		{name: "1ns", d: 1 * time.Nanosecond},
		{name: "500ms", d: 500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.d.String()
			got, rule := parseInterval(raw)
			if rule != "" {
				t.Fatalf("parseInterval(%q) unexpected rule %q", raw, rule)
			}
			if got != tt.d {
				t.Fatalf("parseInterval(%q) = %v, want %v", raw, got, tt.d)
			}
		})
	}
}

func TestParseIntervalAcceptsHumanForms(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{input: "1d", want: 24 * time.Hour},
		{input: "2 days", want: 48 * time.Hour},
		{input: "30min", want: 30 * time.Minute},
		{input: "30 seconds", want: 30 * time.Second},
		{input: "1h30", want: 1*time.Hour + 30*time.Minute},
		{input: "1w", want: 7 * 24 * time.Hour},
		{input: "7d", want: 7 * 24 * time.Hour},
		{input: "1.5h", want: 90 * time.Minute},
		{input: "90m", want: 90 * time.Minute},
		{input: " 2h ", want: 2 * time.Hour},
		{input: "1h30m10s", want: 1*time.Hour + 30*time.Minute + 10*time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, rule := parseInterval(tt.input)
			if rule != "" {
				t.Fatalf("parseInterval(%q) unexpected rule %q", tt.input, rule)
			}
			if got != tt.want {
				t.Fatalf("parseInterval(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseIntervalRefusesBareNumberAndGarbage(t *testing.T) {
	tests := []struct {
		input      string
		wantSubstr string
	}{
		{input: "30", wantSubstr: "Every must be a duration"},
		{input: "", wantSubstr: "Every must be a duration"},
		{input: "not-a-duration", wantSubstr: "Every must be a duration"},
		{input: "0s", wantSubstr: "Every must be positive"},
		{input: "-5m", wantSubstr: "Every must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, rule := parseInterval(tt.input)
			if got != 0 {
				t.Fatalf("parseInterval(%q) = %v, want 0", tt.input, got)
			}
			if !strings.Contains(rule, tt.wantSubstr) {
				t.Fatalf("parseInterval(%q) rule = %q, want rule containing %q", tt.input, rule, tt.wantSubstr)
			}
		})
	}
}

func TestFormatIntervalShortestForm(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "30m", d: 30 * time.Minute, want: "30m"},
		{name: "24h", d: 24 * time.Hour, want: "24h"},
		{name: "48h", d: 48 * time.Hour, want: "2d"},
		{name: "7d", d: 7 * 24 * time.Hour, want: "7d"},
		{name: "30d", d: 30 * 24 * time.Hour, want: "30d"},
		{name: "25h", d: 25 * time.Hour, want: "25h"},
		{name: "90s", d: 90 * time.Second, want: "1m30s"},
		{name: "1h30m", d: 1*time.Hour + 30*time.Minute, want: "1h30m"},
		{name: "1h0m30s", d: 1*time.Hour + 30*time.Second, want: "1h0m30s"},
		{name: "0", d: 0, want: "0s"},
		{name: "500ms", d: 500 * time.Millisecond, want: "500ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInterval(tt.d)
			if got != tt.want {
				t.Fatalf("formatInterval(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestFormatIntervalIsParsableForEveryWholeSecondValue(t *testing.T) {
	values := []time.Duration{
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
		45 * time.Minute,
		25 * time.Hour,
		1*time.Hour + 30*time.Second,
		3661 * time.Second,
	}
	for _, d := range values {
		t.Run(d.String(), func(t *testing.T) {
			formatted := formatInterval(d)
			got, rule := parseInterval(formatted)
			if rule != "" {
				t.Fatalf("parseInterval(formatInterval(%v)=%q) unexpected rule: %q", d, formatted, rule)
			}
			if got != d {
				t.Fatalf("parseInterval(formatInterval(%v)=%q) = %v, want %v", d, formatted, got, d)
			}
		})
	}
}

func TestStepInterval(t *testing.T) {
	tests := []struct {
		name  string
		cur   time.Duration
		delta int
		want  time.Duration
	}{
		{name: "cur 0 seeds defaultInterval (delta 0)", cur: 0, delta: 0, want: defaultInterval},
		{name: "cur 0 seeds defaultInterval (delta +1)", cur: 0, delta: 1, want: defaultInterval},
		{name: "cur 0 seeds defaultInterval (delta -1)", cur: 0, delta: -1, want: defaultInterval},
		{name: "30m step up to 1h", cur: 30 * time.Minute, delta: 1, want: time.Hour},
		{name: "30m step down to 15m", cur: 30 * time.Minute, delta: -1, want: 15 * time.Minute},
		{name: "45m off-ladder step up ties toward travel (1h)", cur: 45 * time.Minute, delta: 1, want: time.Hour},
		{name: "45m off-ladder step down ties toward travel (30m)", cur: 45 * time.Minute, delta: -1, want: 30 * time.Minute},
		{name: "1m bottom rung clamps down", cur: time.Minute, delta: -1, want: time.Minute},
		{name: "30d top rung clamps up", cur: 30 * 24 * time.Hour, delta: 1, want: 30 * 24 * time.Hour},
		{name: "1h step +5 lands 5 rungs away (48h)", cur: time.Hour, delta: 5, want: 48 * time.Hour},
		{name: "48h step -5 lands 5 rungs away (1h)", cur: 48 * time.Hour, delta: -5, want: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stepInterval(tt.cur, tt.delta)
			if got != tt.want {
				t.Fatalf("stepInterval(%v, %d) = %v, want %v", tt.cur, tt.delta, got, tt.want)
			}
		})
	}
}

func TestValidateInterval(t *testing.T) {
	tests := []struct {
		name       string
		d          time.Duration
		wantValid  bool
		wantSubstr string
	}{
		{name: "500ms subsecond invalid", d: 500 * time.Millisecond, wantValid: false, wantSubstr: "whole number of seconds"},
		{name: "0 zero invalid", d: 0, wantValid: false, wantSubstr: "whole number of seconds"},
		{name: "1s min valid", d: time.Second, wantValid: true},
		{name: "90s valid", d: 90 * time.Second, wantValid: true},
		{name: "30d ladder max valid", d: 30 * 24 * time.Hour, wantValid: true},
		// 4000d pins the deliberate NO-upper-bound decision in validateInterval: internal/automation
		// owns the ceiling (config.MaxTimeoutSeconds) and this package cannot import internal/config
		// under .mivia/policy/import-layers.json, so a local ceiling would risk drifting.
		{name: "4000d large value valid (no upper bound)", d: 4000 * 24 * time.Hour, wantValid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateInterval(tt.d)
			if tt.wantValid {
				if got != "" {
					t.Fatalf("validateInterval(%v) = %q, want valid empty string", tt.d, got)
				}
			} else {
				if got == "" || !strings.Contains(got, tt.wantSubstr) {
					t.Fatalf("validateInterval(%v) = %q, want rule containing %q", tt.d, got, tt.wantSubstr)
				}
			}
		})
	}
}

func TestIntervalStatus(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantOK     bool
		wantSubstr string
	}{
		{name: "valid 30m", raw: "30m", wantOK: true, wantSubstr: "30m"},
		{name: "valid 2h", raw: "2h", wantOK: true, wantSubstr: "2h"},
		{name: "valid 1d", raw: "1d", wantOK: true, wantSubstr: "24h"},
		{name: "invalid garbage", raw: "garbage", wantOK: false, wantSubstr: "Every must be a duration"},
		{name: "invalid zero", raw: "0s", wantOK: false, wantSubstr: "Every must be positive"},
		{name: "invalid negative", raw: "-5m", wantOK: false, wantSubstr: "Every must be positive"},
		{name: "invalid subsecond", raw: "500ms", wantOK: false, wantSubstr: "whole number of seconds"},
		{name: "blank", raw: "", wantOK: false, wantSubstr: "Every must be a duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, ok := intervalStatus(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("intervalStatus(%q) ok = %v, want %v (text=%q)", tt.raw, ok, tt.wantOK, text)
			}
			if !strings.Contains(text, tt.wantSubstr) {
				t.Fatalf("intervalStatus(%q) text = %q, want text containing %q", tt.raw, text, tt.wantSubstr)
			}
		})
	}
}

func TestParseIntervalRefusesOverflowingComponents(t *testing.T) {
	overflowCases := []string{
		"9223372036854775808ms 9223372036854775808ms 5m",
		"9223372036854775808ms",
		"106751991d",
		"99999999999999999999h",
		"2147483648d 2147483648d",
	}
	for _, input := range overflowCases {
		t.Run("overflow/"+input, func(t *testing.T) {
			got, rule := parseInterval(input)
			if got != 0 {
				t.Fatalf("parseInterval(%q) = %v, want 0", input, got)
			}
			if rule == "" {
				t.Fatalf("parseInterval(%q) rule is empty, want non-empty parse-failure rule", input)
			}
		})
	}

	t.Run("legitimate large in-range value parses", func(t *testing.T) {
		const validLarge = "106751d"
		want := 106751 * 24 * time.Hour
		got, rule := parseInterval(validLarge)
		if rule != "" {
			t.Fatalf("parseInterval(%q) unexpected rule %q", validLarge, rule)
		}
		if got != want {
			t.Fatalf("parseInterval(%q) = %v, want %v", validLarge, got, want)
		}
	})
}

func TestParseIntervalRefusesSignedHumanComponents(t *testing.T) {
	cases := []string{
		"1h-30m",
		"2h-90m",
		"+1h",
		"1d-1d",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			got, rule := parseInterval(input)
			if got != 0 {
				t.Fatalf("parseInterval(%q) = %v, want 0", input, got)
			}
			if rule == "" {
				t.Fatalf("parseInterval(%q) rule is empty, want non-empty rule", input)
			}
		})
	}
}

func TestFormatIntervalNegativeAndSubsecond(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
	}{
		{name: "negative 5m", d: -5 * time.Minute},
		{name: "negative 1s", d: -1 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInterval(tt.d)
			want := tt.d.String()
			if got != want {
				t.Fatalf("formatInterval(%v) = %q, want %q", tt.d, got, want)
			}
		})
	}
}

func TestStepIntervalOffLadderZeroDelta(t *testing.T) {
	// 45m is exactly between 30m (index 3) and 1h (index 4); ties round up to 1h.
	if got := stepInterval(45*time.Minute, 0); got != time.Hour {
		t.Fatalf("stepInterval(45m, 0) = %v, want %v (ties round up)", got, time.Hour)
	}
	// 44m is nearer to 30m than 1h (14m vs 16m); snaps down to 30m.
	if got := stepInterval(44*time.Minute, 0); got != 30*time.Minute {
		t.Fatalf("stepInterval(44m, 0) = %v, want %v (nearest rung snaps down)", got, 30*time.Minute)
	}
}

func TestStepIntervalOffLadderLargeDelta(t *testing.T) {
	isLadderMember := func(d time.Duration) bool {
		for _, rung := range intervalLadder {
			if d == rung {
				return true
			}
		}
		return false
	}

	deltas := []struct {
		name  string
		delta int
	}{
		{name: "delta +3", delta: 3},
		{name: "delta -3", delta: -3},
		{name: "delta MaxInt", delta: math.MaxInt},
		{name: "delta MinInt", delta: math.MinInt},
	}

	for _, tt := range deltas {
		t.Run(tt.name, func(t *testing.T) {
			got := stepInterval(45*time.Minute, tt.delta)
			if !isLadderMember(got) {
				t.Fatalf("stepInterval(45m, %d) = %v is not a member of intervalLadder", tt.delta, got)
			}
		})
	}
}

func TestParseIntervalTokenizerEdges(t *testing.T) {
	t.Run("case insensitive unit 2 DAYS", func(t *testing.T) {
		got, rule := parseInterval("2 DAYS")
		if rule != "" {
			t.Fatalf("parseInterval(%q) unexpected rule %q", "2 DAYS", rule)
		}
		if got != 48*time.Hour {
			t.Fatalf("parseInterval(%q) = %v, want %v", "2 DAYS", got, 48*time.Hour)
		}
	})

	t.Run("tabs between number and unit", func(t *testing.T) {
		got, rule := parseInterval("1\t\t\th")
		if rule != "" {
			t.Fatalf("parseInterval(%q) unexpected rule %q", "1\t\t\th", rule)
		}
		if got != time.Hour {
			t.Fatalf("parseInterval(%q) = %v, want %v", "1\t\t\th", got, time.Hour)
		}
	})

	t.Run("1ns30 refused (ns has no sub-unit)", func(t *testing.T) {
		got, rule := parseInterval("1ns30")
		if got != 0 {
			t.Fatalf("parseInterval(%q) = %v, want 0", "1ns30", got)
		}
		if rule == "" {
			t.Fatalf("parseInterval(%q) rule is empty, want parse failure rule", "1ns30")
		}
	})

	t.Run("unit without number refused", func(t *testing.T) {
		got, rule := parseInterval("h")
		if got != 0 {
			t.Fatalf("parseInterval(%q) = %v, want 0", "h", got)
		}
		if rule == "" {
			t.Fatalf("parseInterval(%q) rule is empty, want parse failure rule", "h")
		}
	})

	t.Run("number ending in dot without digits refused", func(t *testing.T) {
		got, rule := parseInterval("1.")
		if got != 0 {
			t.Fatalf("parseInterval(%q) = %v, want 0", "1.", got)
		}
		if rule == "" {
			t.Fatalf("parseInterval(%q) rule is empty, want parse failure rule", "1.")
		}
	})
}
