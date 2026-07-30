package cli

import "testing"

// layout() sizes the chat viewport on the Update path; chatViewLayout sizes it
// on the View path. They size the SAME viewport, so they must agree.
//
// When they did not — layout() omitted the composer's padding rows, making its
// height 2 larger — the sequence was: applyFollowScroll runs during Update and
// calls GotoBottom() against the taller height, fixing YOffset; View() then
// shrinks Height without recomputing YOffset, so viewport.View() renders
// lines[YOffset : YOffset+smallerHeight] and the final rows are never painted.
// finishStream is a turn's last render, so the turn footer stayed clipped until
// the user next interacted, which is why the bug read as intermittent.
//
// The assertion is equality between the two computations rather than either one
// matching a constant. Both subtract a growing list of terms — status, live
// panel, composer, hint, padding — and pinning a number would only re-pin
// today's arithmetic. Equality is the actual invariant and survives either side
// changing shape.
//
// Regression: plan 26 B6 (fixed in e335cbe, shipped without this guard).
func TestLayoutViewportHeightMatchesViewComputation(t *testing.T) {
	// A one-line header matches layout()'s statusH == 1 assumption, so a
	// mismatch this test reports is a real divergence, not a header artefact.
	const header = "status"

	for _, termH := range []int{20, 24, 30, 40, 50} {
		m := newTUIModel(makeTestSession(), nil, true)
		m.width, m.height = 80, termH

		// Update path first, then View path — the real order, and it matters:
		// chatViewLayout resizes the textarea as a side effect.
		m.layout()
		updateHeight := m.viewport.Height

		viewHeight := m.chatViewLayout(header, phaseIdle).viewportHeight

		if updateHeight != viewHeight {
			t.Errorf("termH=%d: layout() sized the viewport %d, View() sizes it %d; "+
				"the larger one leaves YOffset past the rendered window and clips the tail",
				termH, updateHeight, viewHeight)
		}
	}
}

// The padding is a shared constant precisely so the two computations cannot
// drift apart again. The test above catches a re-inlined literal that changes
// the value; this one states the intent, so a literal that happens to match
// today still reads as a mistake.
func TestComposerPadRowsCoversBothPaddingRows(t *testing.T) {
	if composerPadRows != 2 {
		t.Fatalf("composerPadRows = %d, want 2 (1 top + 1 bottom around the composer card)", composerPadRows)
	}
}
