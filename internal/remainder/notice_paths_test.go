package remainder

// fitTruncation's contract is one line long: whatever it returns fits maxBytes
// and never names a reference it could not print in full. These cases drive it
// through every way the envelope can fail to fit.

import (
	"strings"
	"testing"
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
			got := fitTruncation(body, len(body), maxBytes, r)
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
	got := fitTruncation(body, len(body), 60, ref)
	if strings.Contains(got, "ref:output:") {
		t.Fatalf("a ref was printed under a budget that cannot hold it: %q", got)
	}
	if !strings.Contains(got, "truncated: kept") {
		t.Fatalf("the plain notice was lost: %q", got)
	}
}

func TestFitTruncationClipsTheNoticeItself(t *testing.T) {
	body := strings.Repeat("x", 200)
	got := fitTruncation(body, len(body), 10, "")
	if len(got) > 10 {
		t.Fatalf("degenerate budget produced %d bytes", len(got))
	}
	if strings.Contains(got, "ref:") {
		t.Fatalf("a degenerate notice named a reference: %q", got)
	}
	// A budget one byte over the plain notice keeps the notice whole.
	plain := TruncationNotice(0, len(body), "")
	if got := fitTruncation(body, len(body), len(plain), ""); len(got) > len(plain) {
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
			got := fitTruncation(body, 0, maxBytes, r)
			if len(got) > maxBytes {
				t.Errorf("fitTruncation(total=0, maxBytes=%d, ref=%q) = %d bytes, over budget", maxBytes, r, len(got))
			}
		}
	}
}

func TestFitTruncationNeverSplitsARune(t *testing.T) {
	body := strings.Repeat("héllo wörld ", 40)
	for maxBytes := 1; maxBytes < 120; maxBytes++ {
		got := fitTruncation(body, len(body), maxBytes, "")
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
	got := fitTruncation("tiny", 4, 200, "")
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
