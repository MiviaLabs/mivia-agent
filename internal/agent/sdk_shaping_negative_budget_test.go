// Tests for the negative BatchResultBudgetBytes carrier on the SDK
// path (item 4 / v4). The legacy `derivedBatchBudget` lives in
// internal/agent/shape_batch.go:505-517 and turns a positive
// MaxContextTokens into a byte budget; the SDK-side equivalent
// `applyTurnShaping` must replicate the same constants
// (bytesPerToken=4, derivedBudgetShare=4, derivedBatchBudgetFloorBytes
// 256 KiB, maxDerivableTokens) so a caller who sets
// BatchResultBudgetBytes: -1 sees real per-batch shaping on the SDK
// path, not the documented "no-op" gap.
//
// These tests pin the BLOCK on `applyTurnShaping`'s `<= 0` early
// return: at construction time today, a negative budget leaves the
// registry unwrapped. That is the bug.
package agent

import (
	"testing"
)

// TestDerivedBatchBudgetContract pins the legacy derivation contract
// at its two policy boundaries. Because `derived` equals
// `tokens * bytesPerToken / derivedBudgetShare` and the two constants
// are equal, the formula reduces to `tokens`, so the floor dominates
// every small-token case and is bypassed only when tokens reach the
// floor itself. A future change to bytesPerToken or
// derivedBudgetShare (intentional, e.g. a calibration refresh) must
// be reflected here.
func TestDerivedBatchBudgetContract(t *testing.T) {
	cases := []struct {
		name string
		tok  int
		want int
	}{
		{"zero is inert", 0, 0},
		{"negative is inert", -1, 0},
		{"1 token below floor clamps to 256 KiB", 1, 256 << 10},
		{"32K tokens below floor clamps to 256 KiB", 32 << 10, 256 << 10},
		{"65K tokens below floor clamps to 256 KiB", 65 << 10, 256 << 10},
		{"256K tokens at the floor returns 256 KiB", 256 << 10, 256 << 10},
		{"257K tokens above the floor derives 257 KiB", 257 << 10, 257 << 10},
		{"1Mi tokens derives 1 MiB", 1 << 20, 1 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivedBatchBudget(tc.tok)
			if got != tc.want {
				t.Fatalf("derivedBatchBudget(%d) = %d, want %d", tc.tok, got, tc.want)
			}
		})
	}
}

// TestApplyTurnShapingHonorsNegativeBudget is the bug-pin: today
// `applyTurnShaping` early-returns on `<= 0`, so a caller who sets
// BatchResultBudgetBytes: -1 sees the full undegarded history on the
// SDK path. The expectation after fix is that the registry IS wrapped
// (the wrapper is present and emitted on every Run).
func TestApplyTurnShapingHonorsNegativeBudget(t *testing.T) {
	// Probe the same batch the legacy path would degrade. With a 32K
	// token budget the derivation gives 256 KiB (floor). Three
	// calls of 200 KiB each must all show up, AND the bytes added to
	// history must stay under the floor + degrade bounds.
	const derivedBudget = 256 << 10
	f := newBatchFixture(t, []int{200 << 10, 200 << 10, 200 << 10})
	loop := f.run(t, Options{
		BatchResultBudgetBytes: -1,
		MaxContextTokens:       32 << 10,
	})
	bodies := toolBodies(loop)
	if len(bodies) != 3 {
		t.Fatalf("got %d tool results, want 3 (negative budget must shape, not drop)", len(bodies))
	}
	total := totalToolBytes(loop)
	bound := derivedBudget + BatchDegradeFloorBytes + 3*(256+statusLineMaxBytes)
	if total > bound {
		t.Fatalf("with negative budget derived to %d, total %d exceeds bound %d", derivedBudget, total, bound)
	}
}

// TestApplyTurnShapingZeroIsInert preserves the v3 contract:
// BatchResultBudgetBytes == 0 means no shaping at all, even after
// the `< 0` derive branch lands.
func TestApplyTurnShapingZeroIsInert(t *testing.T) {
	f := newBatchFixture(t, []int{200 << 10, 200 << 10})
	loop := f.run(t, Options{BatchResultBudgetBytes: 0, MaxContextTokens: 100 << 10})
	total := totalToolBytes(loop)
	if total < 2*(200<<10) {
		t.Fatalf("Budget=0 must be inert; total = %d, want >= 400 KiB", total)
	}
}
