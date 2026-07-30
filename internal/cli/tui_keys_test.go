package cli

import "testing"

func TestRouteFocusKey(t *testing.T) {
	cases := []struct {
		name       string
		from, want tuiFocus
		key        string
		consumed   bool
	}{
		{"tab composer", focusComposer, focusScrollback, "tab", true},
		{"tab scrollback", focusScrollback, focusComposer, "tab", true},
		{"shift+tab composer", focusComposer, focusScrollback, "shift+tab", true},
		{"shift+tab scrollback", focusScrollback, focusComposer, "shift+tab", true},
		{"escape scrollback", focusScrollback, focusComposer, "esc", true},
		{"escape composer", focusComposer, focusComposer, "esc", false},
		{"arrow scrollback", focusScrollback, focusScrollback, "up", true},
		{"arrow composer", focusComposer, focusComposer, "up", false},
		{"page scrollback", focusComposer, focusScrollback, "pgdown", true},
		{"page from scrollback", focusScrollback, focusScrollback, "pgdown", true},
		{"home from scrollback", focusScrollback, focusScrollback, "home", true},
		{"end from scrollback", focusScrollback, focusScrollback, "end", true},
		{"printable from scrollback", focusScrollback, focusComposer, "x", false},
		{"printable from composer", focusComposer, focusComposer, "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.consumed {
				t.Fatalf("routeFocusKey(%v, %q) = (%v, %v), want (%v, %v)", tc.from, tc.key, got, consumed, tc.want, tc.consumed)
			}
		})
	}
}

// runFocusCases is a helper for table-driven focus tests with the 2-pane model.
type focusTestCase struct {
	name         string
	from, want   tuiFocus
	key          string
	wantConsumed bool
}

func runFocusCases(t *testing.T, cases []focusTestCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := routeFocusKey(tc.from, tc.key)
			if got != tc.want || consumed != tc.wantConsumed {
				t.Fatalf("routeFocusKey(%v, %q) = (%v, %v), want (%v, %v)",
					tc.from, tc.key, got, consumed, tc.want, tc.wantConsumed)
			}
		})
	}
}

func addComposerCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"composer tab", focusComposer, focusScrollback, "tab", true},
		focusTestCase{"composer shift+tab", focusComposer, focusScrollback, "shift+tab", true},
		focusTestCase{"composer esc", focusComposer, focusComposer, "esc", false},
		focusTestCase{"composer up", focusComposer, focusComposer, "up", false},
		focusTestCase{"composer down", focusComposer, focusComposer, "down", false},
		focusTestCase{"composer pgup", focusComposer, focusScrollback, "pgup", true},
		focusTestCase{"composer pgdown", focusComposer, focusScrollback, "pgdown", true},
		// home/end stay with the composer: they are its only line-start and
		// line-end keys. handleChatControlKey gives them the transcript
		// meaning when scrollback has focus (or the draft is empty).
		focusTestCase{"composer home", focusComposer, focusComposer, "home", false},
		focusTestCase{"composer end", focusComposer, focusComposer, "end", false},
		focusTestCase{"composer shift+home", focusComposer, focusComposer, "shift+home", true},
		focusTestCase{"composer shift+end", focusComposer, focusComposer, "shift+end", true},
		focusTestCase{"composer printable", focusComposer, focusComposer, "x", false},
	)
}

func addScrollbackCases(dst []focusTestCase) []focusTestCase {
	return append(dst,
		focusTestCase{"scrollback tab", focusScrollback, focusComposer, "tab", true},
		focusTestCase{"scrollback shift+tab", focusScrollback, focusComposer, "shift+tab", true},
		focusTestCase{"scrollback esc", focusScrollback, focusComposer, "esc", true},
		focusTestCase{"scrollback up", focusScrollback, focusScrollback, "up", true},
		focusTestCase{"scrollback down", focusScrollback, focusScrollback, "down", true},
		focusTestCase{"scrollback pgup", focusScrollback, focusScrollback, "pgup", true},
		focusTestCase{"scrollback pgdown", focusScrollback, focusScrollback, "pgdown", true},
		focusTestCase{"scrollback home", focusScrollback, focusScrollback, "home", true},
		focusTestCase{"scrollback end", focusScrollback, focusScrollback, "end", true},
		focusTestCase{"scrollback shift+home", focusScrollback, focusScrollback, "shift+home", true},
		focusTestCase{"scrollback shift+end", focusScrollback, focusScrollback, "shift+end", true},
		focusTestCase{"scrollback printable", focusScrollback, focusComposer, "x", false},
	)
}

func TestRouteFocusKeyFullMatrix(t *testing.T) {
	var all []focusTestCase
	all = addComposerCases(all)
	all = addScrollbackCases(all)
	runFocusCases(t, all)
}
