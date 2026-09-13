package clichat

// tui_focus_coverage_test.go drives the TuiFocus enum helpers in
// tui_focus.go. They were uncovered after the cli split because the
// legacytui tests do not exercise the enum's string cycle or its
// wrap-around transitions directly.

import "testing"

func TestTuiFocusString(t *testing.T) {
	for _, tc := range []struct {
		f    TuiFocus
		want string
	}{
		{FocusComposer, "composer"},
		{FocusScrollback, "scrollback"},
		{TuiFocus(99), "composer"},
	} {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("TuiFocus(%d).String() = %q, want %q", tc.f, got, tc.want)
		}
	}
}

func TestNextTUIFocusWraps(t *testing.T) {
	if got := nextTUIFocus(FocusComposer, false); got != FocusScrollback {
		t.Errorf("nextTUIFocus(composer, fwd) = %v", got)
	}
	if got := nextTUIFocus(FocusScrollback, false); got != FocusComposer {
		t.Errorf("nextTUIFocus(scrollback, fwd) = %v, want wrap", got)
	}
	if got := nextTUIFocus(FocusComposer, true); got != FocusScrollback {
		t.Errorf("nextTUIFocus(composer, rev) = %v, want wrap", got)
	}
	// Unknown focus falls back to FocusComposer.
	if got := nextTUIFocus(TuiFocus(99), false); got != FocusComposer {
		t.Errorf("nextTUIFocus(99) = %v, want composer fallback", got)
	}
}

func TestRouteFocusKey(t *testing.T) {
	for _, tc := range []struct {
		from     TuiFocus
		key      string
		wantTo   TuiFocus
		wantCons bool
	}{
		{FocusComposer, "tab", FocusScrollback, true},
		{FocusScrollback, "shift+tab", FocusComposer, true},
		{FocusScrollback, "esc", FocusComposer, true},
		{FocusComposer, "esc", FocusComposer, false}, // already composer
		{FocusComposer, "up", FocusComposer, false},
		{FocusScrollback, "up", FocusScrollback, true},
		{FocusScrollback, "pgup", FocusScrollback, true},
		{FocusComposer, "a", FocusComposer, false}, // printable
	} {
		got, consumed := RouteFocusKey(tc.from, tc.key)
		if got != tc.wantTo || consumed != tc.wantCons {
			t.Errorf("RouteFocusKey(%v, %q) = (%v, %v), want (%v, %v)",
				tc.from, tc.key, got, consumed, tc.wantTo, tc.wantCons)
		}
	}
}

func TestIsPrintableKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"a", true},
		{"1", true},
		{"é", true},
		{"", false},
		{"ab", false},
		{"up", false},
	} {
		if got := isPrintableKey(tc.key); got != tc.want {
			t.Errorf("isPrintableKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
