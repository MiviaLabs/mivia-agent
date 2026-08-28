package config

import "testing"

// TestDefaultTotalTimeoutExceedsDefaultRequestTimeout pins the ordering
// invariant between the two compiled subagent timeout defaults.
// TotalTimeout bounds the outer context every per-request timeout is
// derived from (context.WithTimeout never extends past its parent's
// deadline), so a compiled total default shorter than the compiled
// request default silently truncates the documented per-request
// allowance: a single legitimate call inside its own 30-minute budget
// gets killed by a sub-30-minute outer deadline first and is misreported
// timed_out, even though it never exceeded its own timeout.
//
// RED: fails while DefaultSubagentTotalTimeoutSec (3600s) is less than
// DefaultSubagentRequestTimeoutSec (1800s).
func TestDefaultTotalTimeoutExceedsDefaultRequestTimeout(t *testing.T) {
	if DefaultSubagentTotalTimeoutSec <= DefaultSubagentRequestTimeoutSec {
		t.Fatalf("DefaultSubagentTotalTimeoutSec (%ds) must exceed DefaultSubagentRequestTimeoutSec (%ds); "+
			"otherwise the outer total budget truncates the documented per-request allowance",
			DefaultSubagentTotalTimeoutSec, DefaultSubagentRequestTimeoutSec)
	}
}
