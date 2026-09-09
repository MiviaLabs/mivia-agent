package tools

import "testing"

// TestSplitCaptureBudgetBoundaries pins every observable boundary in
// splitCaptureBudget.
//
// This repo's own mutation kit found two survived mutants on the first
// exploratory sweep of this package: `max <= 0` -> `max < 0`, and
// `headQuota < 1` -> `headQuota <= 1`. Both were exhaustively checked
// (every max in [-100, 100000]) and are TRUE equivalent mutants: the
// code path each guard skips independently computes the identical
// (headQuota, tailQuota) the guard itself would have returned, so no
// test of this function's output can ever kill them. They are recorded
// in .mivia/policy/mutation/internal_tools.json with that evidence
// rather than left to inflate the survivor count forever, or "killed"
// by a test asserting something the mutation cannot actually change.
//
// What this test DOES catch: any mutation that moves the 1/3-head split
// point, drops the tiny-budget floor for a genuinely reachable case
// (max in {2,3,4}, where the lift changes the real output), or loses
// bytes from the total budget.
func TestSplitCaptureBudgetBoundaries(t *testing.T) {
	cases := []struct {
		max                  int
		headQuota, tailQuota int
	}{
		{max: -1, headQuota: 0, tailQuota: 0},
		{max: 0, headQuota: 0, tailQuota: 0},
		// max/3 == 0, and max < 2: no floor applies, head stays 0.
		{max: 1, headQuota: 0, tailQuota: 1},
		// max/3 == 0, max >= 2: the tiny-budget floor lifts head to 1.
		// This is the one case that actually observes the floor firing.
		{max: 2, headQuota: 1, tailQuota: 1},
		// max/3 == 1 already: the floor's condition is false and must
		// stay false (nothing to lift).
		{max: 3, headQuota: 1, tailQuota: 2},
		{max: 4, headQuota: 1, tailQuota: 3},
		{max: 9, headQuota: 3, tailQuota: 6},
	}
	for _, tc := range cases {
		head, tail := splitCaptureBudget(tc.max)
		if head != tc.headQuota || tail != tc.tailQuota {
			t.Errorf("splitCaptureBudget(%d) = (%d, %d), want (%d, %d)",
				tc.max, head, tail, tc.headQuota, tc.tailQuota)
		}
		if tc.max > 0 && head+tail != tc.max {
			t.Errorf("splitCaptureBudget(%d): head+tail = %d, want %d (budget must not be lost or exceeded)",
				tc.max, head+tail, tc.max)
		}
	}
}
