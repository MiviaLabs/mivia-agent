package agent

// Once both the primary turn budget (remaining) and the tail-preview reserve
// (previewReserve) are spent, shapeOne used to collapse EVERY further result
// to a bare "kept 0 of N bytes" notice, no matter how small - even a 955-byte
// grep match got zero bytes of visible content and a forced read_output
// round trip. These tests pin the fix: a small, fixed, per-result floor
// funded outside the reserve so the tail of a very long turn still shows
// something, without reopening the reserve's own aggregate bound.

import (
	"strings"
	"testing"
)

func TestShapeOneReserveExhaustedSmallBodyStillShowsContent(t *testing.T) {
	spool, _ := testSpool(t)
	p := capPart(t, spool, body(955), 0)
	p.toolName = "grep"

	got, _, degraded, previewUsed := shapeOne(p, 0, 0, newShapeEnv(spool, shapeTestPrincipal))

	if !degraded {
		t.Fatalf("expected a degrade once both budgets are exhausted, got a passthrough: %q", tailOf(got))
	}
	if strings.Contains(got, "kept 0 of") {
		t.Errorf("reserve-exhausted small body still collapsed to a bare \"kept 0\" notice: %q", got)
	}
	if previewUsed != 0 {
		t.Errorf("final-floor tier must not draw from previewReserve, got previewUsed=%d", previewUsed)
	}
}

func TestShapeOneReserveExhaustedLargeBodyStaysBounded(t *testing.T) {
	spool, _ := testSpool(t)
	p := capPart(t, spool, body(300<<10), 0)
	p.toolName = "read_file"

	got, _, degraded, _ := shapeOne(p, 0, 0, newShapeEnv(spool, shapeTestPrincipal))

	if !degraded {
		t.Fatalf("expected a degrade for a 300 KiB body, got a passthrough")
	}
	if len(got) > finalPreviewFloorBytes+512 {
		// +512 is slack for the notice's own framing (ref + kept/total digits),
		// not a second content allowance - the floor still bounds this tier.
		t.Errorf("final-floor degrade produced %d bytes, want roughly bounded by finalPreviewFloorBytes=%d", len(got), finalPreviewFloorBytes)
	}
}

func TestShapeOneReserveExhaustedRespectsEffectiveCap(t *testing.T) {
	spool, _ := testSpool(t)
	// effectiveCap (256) is below finalPreviewFloorBytes: whichever tier
	// handles this (the final floor, or the unconditional free pass a
	// pass-1-capped body this small can already qualify for), the result
	// must never exceed what pass 1 itself would have produced under a 256
	// B cap (F3) - a floor sized above the cap must not let it through
	// larger than the cap.
	p := capPart(t, spool, body(2000), 256)
	p.toolName = "grep"

	got, _, _, _ := shapeOne(p, 0, 0, newShapeEnv(spool, shapeTestPrincipal))

	if len(got) > 256+256 {
		// Slack for a notice's own framing (ref + kept/total digits), not a
		// second content allowance.
		t.Errorf("degrade produced %d bytes, exceeded the tool's effectiveCap=256 by more than framing slack", len(got))
	}
}

func TestShapeOneReserveExhaustedFloorTierItselfRespectsEffectiveCap(t *testing.T) {
	spool, _ := testSpool(t)
	// effectiveCap=1500 sits ABOVE finalPreviewFloorBytes(512) but below the
	// 2000-byte body, so pass 1's own cap does not shrink the body small
	// enough to take the unconditional free pass - this exercises the new
	// tier's own effectiveCap clamp directly, unlike the smaller-cap case
	// above which pass 1 had already resolved on its own.
	p := capPart(t, spool, body(2000), 1500)
	p.toolName = "grep"

	got, _, degraded, previewUsed := shapeOne(p, 0, 0, newShapeEnv(spool, shapeTestPrincipal))

	if !degraded {
		t.Fatalf("expected the final-floor tier to fire, got a passthrough")
	}
	if previewUsed != 0 {
		t.Errorf("final-floor tier must not draw from previewReserve, got previewUsed=%d", previewUsed)
	}
	if len(got) > finalPreviewFloorBytes+256 {
		t.Errorf("final-floor degrade produced %d bytes, want bounded by finalPreviewFloorBytes=%d despite the larger effectiveCap", len(got), finalPreviewFloorBytes)
	}
}

func TestShapeOneReserveExhaustedTinyBodyUnaffected(t *testing.T) {
	// A body under the existing unconditional 2*minNotice free-pass threshold
	// must keep passing through free - the new floor tier must not make this
	// case worse by intercepting it first.
	spool, _ := testSpool(t)
	p := capPart(t, spool, body(50), 0)
	p.toolName = "grep"

	got, _, degraded, _ := shapeOne(p, 0, 0, newShapeEnv(spool, shapeTestPrincipal))

	if degraded {
		t.Errorf("a tiny body should still take the unconditional free pass, got degraded=true body=%q", got)
	}
	if got != body(50) {
		t.Errorf("tiny body was altered: got %q", got)
	}
}
