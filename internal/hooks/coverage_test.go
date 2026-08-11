package hooks

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAppendBoundedPreservesBoundaries(t *testing.T) {
	var body strings.Builder
	if appendBounded(&body, "") || body.Len() != 0 {
		t.Fatal("empty text must not change the context")
	}
	body.WriteString("first")
	if appendBounded(&body, "second") || body.String() != "first\nsecond" {
		t.Fatalf("normal append = %q", body.String())
	}
	body.Reset()
	body.WriteString(strings.Repeat("x", MaxOutputBytes))
	if !appendBounded(&body, "later") || body.Len() != MaxOutputBytes {
		t.Fatal("a full context must report truncation without growing")
	}
	body.Reset()
	body.WriteString(strings.Repeat("x", MaxOutputBytes-1))
	if !appendBounded(&body, "é") || body.Len() != MaxOutputBytes {
		t.Fatalf("bounded unicode append length = %d", body.Len())
	}
}

func TestProtocolEdgeHelpers(t *testing.T) {
	if got := bound("é", 10); got != "é" {
		t.Errorf("bound with oversized limit = %q", got)
	}
	if got := truncateAtRuneBoundary("éx", 1); got != "" {
		t.Errorf("partial rune truncation = %q, want empty", got)
	}
	if warning := warnIf(execution{program: "/tmp/hook.sh"}, "did nothing"); len(warning) != 1 || warning[0] != "hook /tmp/hook.sh did nothing" {
		t.Errorf("warning without stderr = %#v", warning)
	}

	flat := parsePreToolUse(execution{program: "/tmp/hook.sh"}, `{"decision":"deny"}`)
	if !flat.denied || !strings.Contains(flat.reason, "flat") {
		t.Fatalf("flat PreToolUse decision = %#v", flat)
	}
	updated := parseReactive(execution{program: "/tmp/hook.sh"}, `{"updatedInput":{"x":1}}`)
	if len(updated.warnings) != 1 {
		t.Fatalf("reactive updatedInput warnings = %#v", updated.warnings)
	}
}

func TestHookParserDirectInputShapes(t *testing.T) {
	rows, err := handlerRows([]map[string]any{{"argv": []any{"./hook"}}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("typed handler rows = %#v, %v", rows, err)
	}
	if _, err := handlerRows([]any{"not a table"}); err == nil {
		t.Fatal("non-table handler row must fail")
	}
	argv, err := parseArgv([]string{"./hook", "--flag"})
	if err != nil || strings.Join(argv, " ") != "./hook --flag" {
		t.Fatalf("typed argv = %#v, %v", argv, err)
	}
	if _, err := parseTimeout("10", time.Second); err == nil {
		t.Fatal("non-integer timeout must fail")
	}
	// float64 whole numbers are accepted by parseTimeout.
	if d, err := parseTimeout(float64(10), 0); err != nil || d != 10*time.Second {
		t.Fatalf("float64 10.0 timeout: %v, %v", d, err)
	}
	// float64 non-integers are rejected.
	if _, err := parseTimeout(float64(10.5), 0); err == nil {
		t.Fatal("non-integer float64 timeout must fail")
	}
	// float64 edge: 0 is below MinTimeout (1s).
	if _, err := parseTimeout(float64(0), 0); err == nil {
		t.Fatal("float64 0.0 timeout must fail (below MinTimeout)")
	}
	// int64 is still accepted.
	if d, err := parseTimeout(int64(10), 0); err != nil || d != 10*time.Second {
		t.Fatalf("int64 10 timeout: %v, %v", d, err)
	}
	if _, err := parseOnTimeout(true, OnTimeoutBlock); err == nil {
		t.Fatal("non-string on_timeout must fail")
	}
	if _, _, err := parseMatcher(12); err == nil {
		t.Fatal("non-string matcher must fail")
	}
}

// Unit-level tests for parseReactive PreToolUse shape detection.
func TestParseReactiveDetectsPreToolUseShape(t *testing.T) {
	t.Run("deny", func(t *testing.T) {
		v := parseReactive(execution{program: "/tmp/h.sh"}, `{"hookSpecificOutput":{"permissionDecision":"deny"}}`)
		if v.denied {
			t.Fatal("reactive must never set denied")
		}
		found := false
		for _, w := range v.warnings {
			if strings.Contains(w, "PreToolUse") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected warning naming PreToolUse shape, got %v", v.warnings)
		}
		if !strings.Contains(v.context, "hookSpecificOutput") {
			t.Fatalf("raw body must be preserved as context, got %q", v.context)
		}
	})
	t.Run("allow", func(t *testing.T) {
		v := parseReactive(execution{program: "/tmp/h.sh"}, `{"hookSpecificOutput":{"permissionDecision":"allow"}}`)
		if v.denied {
			t.Fatal("reactive must never set denied")
		}
		found := false
		for _, w := range v.warnings {
			if strings.Contains(w, "PreToolUse") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected warning naming PreToolUse shape, got %v", v.warnings)
		}
	})
	t.Run("empty_hook_specific_output", func(t *testing.T) {
		v := parseReactive(execution{program: "/tmp/h.sh"}, `{"hookSpecificOutput":{}}`)
		// HookSpecificOutput present but empty is not a decision shape, so
		// no PreToolUse-shape warning. It falls through as unrecognized JSON.
		for _, w := range v.warnings {
			if strings.Contains(w, "PreToolUse") {
				t.Fatalf("empty hookSpecificOutput must not trigger PreToolUse warning, got %v", v.warnings)
			}
		}
	})
}

// Correct reactive shape must produce no warning at the unit level.
func TestParseReactiveCorrectShapeNoWarning(t *testing.T) {
	v := parseReactive(execution{program: "/tmp/h.sh"}, `{"decision":"block","reason":"x"}`)
	if v.denied {
		t.Fatal("reactive must never set denied")
	}
	for _, w := range v.warnings {
		if strings.Contains(w, "PreToolUse") {
			t.Fatalf("correct reactive shape must not warn about PreToolUse, got %v", v.warnings)
		}
	}
	if !strings.Contains(v.context, "x") {
		t.Fatalf("context must contain the reason, got %q", v.context)
	}
}

// parseTimeout must range-check the DECLARED seconds before multiplying into a
// time.Duration. time.Duration is int64, so the multiplication wraps modulo
// 2^64: (2^55+1)*10^9 ≡ 10^9 (mod 2^64), i.e. a huge declared value wraps onto
// exactly 1s = MinTimeout and used to pass the post-multiplication check.
func TestParseTimeoutRejectsOverflowThatWrapsIntoRange(t *testing.T) {
	overflow := int64(1)<<55 + 1
	d, err := parseTimeout(overflow, 0)
	if err == nil {
		t.Fatalf("parseTimeout(%d) must error, got %v", overflow, d)
	}
	if d == MinTimeout {
		t.Fatalf("parseTimeout(%d) must not resolve to MinTimeout", overflow)
	}
	// The declared bounds are still accepted exactly.
	if d, err := parseTimeout(int64(1), 0); err != nil || d != MinTimeout {
		t.Fatalf("parseTimeout(1) = %v, %v; want MinTimeout", d, err)
	}
	if d, err := parseTimeout(int64(600), 0); err != nil || d != MaxTimeout {
		t.Fatalf("parseTimeout(600) = %v, %v; want MaxTimeout", d, err)
	}
	// Out-of-range declared seconds error, including values that would wrap
	// onto an in-range duration.
	for _, bad := range []int64{0, 601, -1, math.MaxInt64} {
		if d, err := parseTimeout(bad, 0); err == nil {
			t.Errorf("parseTimeout(%d) must error, got %v", bad, d)
		}
	}
}

// Both deny paths that splice hook-controlled text into the model-visible
// reason must stay within maxReasonBytes plus the fixed truncation notice. Hook
// stdout is captured up to MaxOutputBytes (8 KiB), which exceeds maxReasonBytes
// (4 KiB), so a >4 KiB decision string used to reach the model whole.
func TestPreToolUseDenyReasonsStayBounded(t *testing.T) {
	notice := fmt.Sprintf("\n... truncated at %d bytes", maxReasonBytes)
	limit := maxReasonBytes + len(notice)
	huge := strings.Repeat("x", 5<<10)
	cases := []struct {
		name string
		body string
	}{
		{"flat decision", `{"decision":"` + huge + `"}`},
		{"nested permissionDecision", `{"hookSpecificOutput":{"permissionDecision":"` + huge + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := parsePreToolUse(execution{program: "/tmp/h.sh"}, tc.body)
			if !v.denied {
				t.Fatalf("must deny: %#v", v)
			}
			if len(v.reason) > limit {
				t.Fatalf("deny reason length = %d, want <= %d", len(v.reason), limit)
			}
			if !strings.Contains(v.reason, "truncated at") {
				t.Fatalf("deny reason must carry the truncation notice, got %q", v.reason)
			}
		})
	}
	// A rune-heavy decision truncates on a rune boundary, so the reason stays
	// valid UTF-8 for the model payload.
	runeHeavy := strings.Repeat("é", 5<<10)
	v := parsePreToolUse(execution{program: "/tmp/h.sh"}, `{"hookSpecificOutput":{"permissionDecision":"`+runeHeavy+`"}}`)
	if !v.denied {
		t.Fatalf("rune-heavy decision must deny: %#v", v)
	}
	if !utf8.ValidString(v.reason) {
		t.Fatalf("rune-heavy deny reason must be valid UTF-8: %q", v.reason)
	}
	if len(v.reason) > limit {
		t.Fatalf("rune-heavy deny reason length = %d, want <= %d", len(v.reason), limit)
	}
}

// A hook whose stdout overruns the capture bound but trims to empty (whitespace
// only) must still announce the cut. Every other over-budget path reaches the
// caller through verdict.truncated -> Runner.Run's finishContext; the empty-body
// branch used to drop the flag, so the caller never learned the output was cut
// (DC-9 silent truncation).
func TestParseStdoutWhitespaceOnlyOverBudgetAnnouncesTruncation(t *testing.T) {
	for _, event := range []Event{EventPostToolUse, EventPreToolUse} {
		t.Run(string(event), func(t *testing.T) {
			v := parseStdout(event, execution{stdout: []byte(strings.Repeat(" ", 20000)), truncated: true})
			if !v.truncated {
				t.Fatalf("whitespace-only over-budget stdout must keep the truncation flag: %#v", v)
			}
			if v.denied {
				t.Fatalf("an exit-0 no-decision body must stay allow: %#v", v)
			}
		})
	}
	t.Run("nothing cut", func(t *testing.T) {
		v := parseStdout(EventPostToolUse, execution{stdout: nil, truncated: false})
		if v.truncated || v.denied || v.context != "" || len(v.warnings) != 0 {
			t.Fatalf("empty stdout with nothing cut must announce nothing: %#v", v)
		}
	})
	t.Run("under-bound whitespace", func(t *testing.T) {
		v := parseStdout(EventPostToolUse, execution{stdout: []byte(strings.Repeat(" ", 100)), truncated: false})
		if v.truncated || v.denied || v.context != "" || len(v.warnings) != 0 {
			t.Fatalf("under-bound whitespace is not a cut and must announce nothing: %#v", v)
		}
	})
}
