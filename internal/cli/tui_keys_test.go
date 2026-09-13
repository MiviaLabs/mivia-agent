package cli

import "testing"

func TestRouteFocusKey(t *testing.T) {
	cases := []struct {
		name       string
		from, want TuiFocus
		key        string
		consumed   bool
	}{
		{"tab composer", FocusComposer, FocusScrollback, "tab", true},
		{"tab scrollback", FocusScrollback, FocusComposer, "tab", true},
		{"shift+tab composer", FocusComposer, FocusScrollback, "shift+tab", true},
		{"shift+tab scrollback", FocusScrollback, FocusComposer, "shift+tab", true},
		{"escape scrollback", FocusScrollback, FocusComposer, "esc", true},
		{"escape composer", FocusComposer, FocusComposer, "esc", false},
		{"arrow scrollback", FocusScrollback, FocusScrollback, "up", true},
		{"arrow composer", FocusComposer, FocusComposer, "up", false},
		{"page scrollback", FocusComposer, FocusScrollback, "pgdown", true},
		{"page from scrollback", FocusScrollback, FocusScrollback, "pgdown", true},
		{"home from scrollback", FocusScrollback, FocusScrollback, "home", true},
		{"end from scrollback", FocusScrollback, FocusScrollback, "end", true},
		{"printable from scrollback", FocusScrollback, FocusComposer, "x", false},
		{"printable from composer", FocusComposer, FocusComposer, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := RouteFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.consumed {
				t.Fatalf("RouteFocusKey(%v, %q) = (%v, %v), want (%v, %v)", tc.from, tc.key, got, consumed, tc.want, tc.consumed)
			}
		})
	}
}

// runFocusCases is a helper for table-driven focus tests with the 2-pane model.
type focusTestCase struct {
	name         string
	from, want   TuiFocus
	key          string
	wantConsumed bool
}

func runFocusCases(t *testing.T, cases []focusTestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := RouteFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.wantConsumed {
				t.Fatalf("RouteFocusKey(%v, %q) = (%v, %v), want (%v, %v)",
					tc.from, tc.key, got, consumed, tc.want, tc.wantConsumed)
			}
		})
	}
}

func addComposerCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"composer tab", FocusComposer, FocusScrollback, "tab", true},
		focusTestCase{"composer shift+tab", FocusComposer, FocusScrollback, "shift+tab", true},
		focusTestCase{"composer esc", FocusComposer, FocusComposer, "esc", false},
		focusTestCase{"composer up", FocusComposer, FocusComposer, "up", false},
		focusTestCase{"composer down", FocusComposer, FocusComposer, "down", false},
		focusTestCase{"composer pgup", FocusComposer, FocusScrollback, "pgup", true},
		focusTestCase{"composer pgdown", FocusComposer, FocusScrollback, "pgdown", true},
		// home/end stay with the composer: they are its only line-start and
		// line-end keys. handleChatControlKey gives them the transcript
		// meaning when scrollback has focus (or the draft is empty).
		focusTestCase{"composer home", FocusComposer, FocusComposer, "home", false},
		focusTestCase{"composer end", FocusComposer, FocusComposer, "end", false},
		focusTestCase{"composer shift+home", FocusComposer, FocusComposer, "shift+home", true},
		focusTestCase{"composer shift+end", FocusComposer, FocusComposer, "shift+end", true},
		focusTestCase{"composer printable", FocusComposer, FocusComposer, "x", false},
	)
}

func addScrollbackCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"scrollback tab", FocusScrollback, FocusComposer, "tab", true},
		focusTestCase{"scrollback shift+tab", FocusScrollback, FocusComposer, "shift+tab", true},
		focusTestCase{"scrollback esc", FocusScrollback, FocusComposer, "esc", true},
		focusTestCase{"scrollback up", FocusScrollback, FocusScrollback, "up", true},
		focusTestCase{"scrollback down", FocusScrollback, FocusScrollback, "down", true},
		focusTestCase{"scrollback pgup", FocusScrollback, FocusScrollback, "pgup", true},
		focusTestCase{"scrollback pgdown", FocusScrollback, FocusScrollback, "pgdown", true},
		focusTestCase{"scrollback home", FocusScrollback, FocusScrollback, "home", true},
		focusTestCase{"scrollback end", FocusScrollback, FocusScrollback, "end", true},
		focusTestCase{"scrollback shift+home", FocusScrollback, FocusScrollback, "shift+home", true},
		focusTestCase{"scrollback shift+end", FocusScrollback, FocusScrollback, "shift+end", true},
		focusTestCase{"scrollback printable", FocusScrollback, FocusComposer, "x", false},
	)
}

func TestRouteFocusKeyFullMatrix(t *testing.T) {
	var all []focusTestCase
	all = addComposerCases(all)
	all = addScrollbackCases(all)
	runFocusCases(t, all)
}
