package remainder

// fitTruncation's contract is one line long: whatever it returns fits maxBytes
// and never names a reference it could not print in full. These cases drive it
// through every way the envelope can fail to fit.

import (
	"strings"
	"testing"
	"time"
)

func TestCapWithSpoolLeavesShortResultsAlone(t *testing.T) {
	out, truncated := CapWithSpool(nil, "p", "short", 0)
	if truncated || out != "short" {
		t.Fatalf("uncapped = %q, %v", out, truncated)
	}
	if out, truncated := CapWithSpool(nil, "p", "short", 100); truncated || out != "short" {
		t.Fatalf("under budget = %q, %v", out, truncated)
	}
}

func TestFitTruncationAlwaysFitsItsBudget(t *testing.T) {
	body := strings.Repeat("abcdefghij", 40)
	ref := "ref:output:" + strings.Repeat("a", 64)
	for _, maxBytes := range []int{1, 5, 20, 40, 60, 80, 120, 200, 399} {
		for _, r := range []string{"", ref} {
			got := fitTruncation(body, len(body), maxBytes, r, "")
			if len(got) > maxBytes {
				t.Errorf("fitTruncation(maxBytes=%d, ref=%q) = %d bytes, over budget", maxBytes, r, len(got))
			}
			if r != "" && strings.Contains(got, "remainder:") && !strings.Contains(got, r) {
				t.Errorf("fitTruncation(maxBytes=%d) printed a partial reference: %q", maxBytes, got)
			}
		}
	}
}

func TestFitTruncationDropsARefItCannotPrintWhole(t *testing.T) {
	body := strings.Repeat("x", 200)
	ref := "ref:output:" + strings.Repeat("a", 64)
	// Room for a plain notice and some body, but not for the ref notice.
	got := fitTruncation(body, len(body), 60, ref, "")
	if strings.Contains(got, "ref:output:") {
		t.Fatalf("a ref was printed under a budget that cannot hold it: %q", got)
	}
	if !strings.Contains(got, "truncated: kept") {
		t.Fatalf("the plain notice was lost: %q", got)
	}
}

func TestFitTruncationNamesARefThatFitsExactly(t *testing.T) {
	// The bug: when maxBytes equals the full ref notice exactly, the
	// degenerate guard ('noticeBudget >= maxBytes') dropped the ref even
	// though the notice fits - so the stored remainder became unreachable.
	// The fixed guard ('>') keeps the ref at the exact boundary.
	ref := "ref:output:" + strings.Repeat("a", 64)
	body := strings.Repeat("x", 200)
	total := len(body)
	maxBytes := len(TruncationNotice(total, total, ref))

	got := fitTruncation(body, total, maxBytes, ref, "")
	if len(got) > maxBytes {
		t.Fatalf("fitTruncation produced %d bytes, want <= %d", len(got), maxBytes)
	}
	if !strings.Contains(got, ref) {
		t.Fatalf("full ref was not named when it fits exactly: %q", got)
	}
	if !strings.Contains(got, "use read_output") {
		t.Fatalf("missing read_output guidance: %q", got)
	}
	if !strings.Contains(got, "truncated: kept") {
		t.Fatalf("truncation not reported: %q", got)
	}
}

func TestFitTruncationBoundaryProbes(t *testing.T) {
	// DC-6 probe discipline around the exact-fit boundary: 0, 1, max-1, max,
	// max+1, plus an empty-body exact fit. Every case must stay in budget and
	// never emit a partial ref.
	ref := "ref:output:" + strings.Repeat("a", 64)
	body := strings.Repeat("x", 200)
	total := len(body)
	boundary := len(TruncationNotice(total, total, ref))

	cases := []struct {
		name     string
		body     string
		total    int
		maxBytes int
		ref      string
		wantRef  bool
	}{
		{"boundary-1 drops the ref", body, total, boundary - 1, ref, false},
		{"boundary names the ref in full", body, total, boundary, ref, true},
		{"boundary+1 names the ref with content", body, total, boundary + 1, ref, true},
		{"empty-body exact fit", "", 0, len(TruncationNotice(0, 0, ref)), ref, true},
		{"degenerate zero", body, total, 0, ref, false},
		{"degenerate one", body, total, 1, ref, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitTruncation(tc.body, tc.total, tc.maxBytes, tc.ref, "")
			if len(got) > tc.maxBytes {
				t.Fatalf("len(got)=%d > maxBytes=%d: %q", len(got), tc.maxBytes, got)
			}
			if strings.Contains(got, "ref:") && !strings.Contains(got, tc.ref) {
				t.Fatalf("partial ref emitted: %q", got)
			}
			if tc.wantRef {
				if !strings.Contains(got, tc.ref) {
					t.Fatalf("expected the full ref, got %q", got)
				}
			} else if strings.Contains(got, "ref:output:") {
				t.Fatalf("expected no ref, got %q", got)
			}
		})
	}
}

func TestFitTruncationClipsTheNoticeItself(t *testing.T) {
	body := strings.Repeat("x", 200)
	got := fitTruncation(body, len(body), 10, "", "")
	if len(got) > 10 {
		t.Fatalf("degenerate budget produced %d bytes", len(got))
	}
	if strings.Contains(got, "ref:") {
		t.Fatalf("a degenerate notice named a reference: %q", got)
	}
	// A budget one byte over the plain notice keeps the notice whole.
	plain := TruncationNotice(0, len(body), "")
	if got := fitTruncation(body, len(body), len(plain), "", ""); len(got) > len(plain) {
		t.Fatalf("exact-notice budget produced %d bytes, want <= %d", len(got), len(plain))
	}
}

func TestFitTruncationHandlesAKeptCountWiderThanItsReserve(t *testing.T) {
	// A reported total smaller than the body makes the kept-count digits grow
	// past the reserved notice width - the case the refinement loops exist for.
	body := strings.Repeat("x", 500)
	ref := "ref:output:" + strings.Repeat("a", 64)
	for _, maxBytes := range []int{50, 120, 300} {
		for _, r := range []string{"", ref} {
			got := fitTruncation(body, 0, maxBytes, r, "")
			if len(got) > maxBytes {
				t.Errorf("fitTruncation(total=0, maxBytes=%d, ref=%q) = %d bytes, over budget", maxBytes, r, len(got))
			}
		}
	}
}

func TestFitTruncationNeverSplitsARune(t *testing.T) {
	body := strings.Repeat("héllo wörld ", 40)
	for maxBytes := 1; maxBytes < 120; maxBytes++ {
		got := fitTruncation(body, len(body), maxBytes, "", "")
		if len(got) > maxBytes {
			t.Fatalf("maxBytes=%d produced %d bytes", maxBytes, len(got))
		}
		if !strings.HasPrefix(got, "\n") && strings.ContainsRune(got, '�') {
			t.Fatalf("maxBytes=%d split a rune: %q", maxBytes, got)
		}
	}
}

func TestFitTruncationOnABodyShorterThanItsBudget(t *testing.T) {
	// Reached only directly: CapWithSpool filters this case out beforehand.
	got := fitTruncation("tiny", 4, 200, "", "")
	if !strings.HasPrefix(got, "tiny") {
		t.Fatalf("short body was cut: %q", got)
	}
}

func TestTrimPartialRuneDropsOnlyTheBrokenTail(t *testing.T) {
	whole := "héllo"
	if got := trimPartialRune(whole); got != whole {
		t.Fatalf("valid string trimmed: %q", got)
	}
	broken := whole[:len(whole)-1] // cuts the trailing byte of no rune, but 'é' spans 2
	if got := trimPartialRune("h\xc3"); got != "h" {
		t.Fatalf("trimPartialRune(%q) = %q, want %q", "h\xc3", got, "h")
	}
	if got := trimPartialRune(broken); got != broken {
		t.Fatalf("a valid prefix was trimmed: %q", got)
	}
}

func TestCapWithSpoolTrimsLargeInvalidBodyInLinearTime(t *testing.T) {
	// P-1 regression: trimPartialRune re-validated the whole string on every
	// one-byte chop. utf8.ValidString short-circuits at the first invalid
	// byte, so the old loop's per-chop cost is bounded by the invalid byte's
	// *position*, not by how much tail remains - a test with the invalid
	// byte near the start (as an earlier draft of this test had it) stays
	// fast even on the buggy code and proves nothing. To actually exercise
	// the O(n^2) blowup, the invalid byte must sit deep in the string, so
	// each of the many chops needed re-scans a large valid prefix. The fix
	// walks runes forward exactly once, so cost here must stay linear
	// regardless of where the invalid byte sits.
	const prefixLen = 300_000
	const tailLen = 300_000
	body := strings.Repeat("a", prefixLen) + "\xff" + strings.Repeat("b", tailLen)
	// The cap is inclusive (len(result) <= maxBytes returns untruncated), so
	// the cap must sit strictly below the body length for the body to be
	// over-cap and actually exercise the trim path.
	maxBytes := len(body) - 1
	start := time.Now()
	out, truncated := CapWithSpool(nil, "p", body, maxBytes)
	elapsed := time.Since(start)

	if !truncated {
		t.Fatalf("over-cap body was not truncated")
	}
	if !strings.HasPrefix(out, strings.Repeat("a", prefixLen)) {
		t.Fatalf("kept prefix lost: %q…", out[:min(len(out), 40)])
	}
	if strings.Contains(out, "\xff") {
		t.Fatalf("invalid byte survived trimming")
	}
	if len(out) > maxBytes {
		t.Fatalf("len(out)=%d > cap=%d", len(out), maxBytes)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("linear trim took %v; O(n^2) chop re-scan regression", elapsed)
	}
}
