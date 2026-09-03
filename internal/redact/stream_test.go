package redact

import (
	"strings"
	"testing"
)

// The safety property this file exists for.
//
// Streaming a redacted transcript is only safe if the redaction boundary is
// bigger than the fragment boundary. Redacting each delta on its own is not:
// `xk-tok-` at the end of one delta and the rest at the start of the next
// matches no pattern in either half, and the secret reaches the wire in two
// pieces a viewer concatenates back together. Every test below is a way of
// asking the same question - does the split change what ships?

// withPolicy installs a policy for the duration of a test.
func withPolicy(t *testing.T, patterns []string) {
	t.Helper()
	p, err := Compile(patterns, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	old := Current()
	SetPolicy(p)
	t.Cleanup(func() { SetPolicy(old) })
}

// pushAll streams the fragments through one Stream and returns everything that
// reached the wire, in order, including the final flush.
func pushAll(fragments []string) string {
	var s Stream
	var out strings.Builder
	for _, f := range fragments {
		out.WriteString(s.Push(f))
	}
	out.WriteString(s.Flush())
	return out.String()
}

// A synthetic credential. A real vendor prefix would trip the repo's secret
// scanner in CI; the hazard under test is the SPLIT, not the vendor.
const antKey = `xk-tok-api03-AAAABBBBCCCCDDDD`

var keyPattern = []string{`xk-tok-[A-Za-z0-9-]{8,64}`}

// TestASecretSplitAcrossTwoFragmentsIsRedacted is the decisive test. Redacting
// each fragment independently ships both halves verbatim.
func TestASecretSplitAcrossTwoFragmentsIsRedacted(t *testing.T) {
	withPolicy(t, keyPattern)

	got := pushAll([]string{"the key is xk-tok-", "api03-AAAABBBBCCCCDDDD, keep it safe"})

	if strings.Contains(got, "xk-tok-") {
		t.Fatalf("the split secret reached the wire: %q", got)
	}
	if want := "the key is [redacted], keep it safe"; got != want {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

// TestAMatchSpanningThreeFragmentsIsRedacted extends the same hazard past a
// single boundary: a two-fragment lookback is not enough, the whole open block
// has to stay joined until it can no longer grow a match.
func TestAMatchSpanningThreeFragmentsIsRedacted(t *testing.T) {
	withPolicy(t, keyPattern)

	got := pushAll([]string{"key: xk-", "tok-api03-AAAABBBB", "CCCCDDDD done"})

	if strings.Contains(got, "xk-tok") || strings.Contains(got, "AAAABBBB") {
		t.Fatalf("a three-fragment secret reached the wire: %q", got)
	}
	if want := "key: [redacted] done"; got != want {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

// TestASecretArrivingOneCharacterPerFragmentIsRedacted is the degenerate case
// a token-by-token provider actually produces.
func TestASecretArrivingOneCharacterPerFragmentIsRedacted(t *testing.T) {
	withPolicy(t, keyPattern)

	text := "before " + antKey + " after"
	fragments := make([]string, 0, len(text))
	for _, r := range text {
		fragments = append(fragments, string(r))
	}

	got := pushAll(fragments)

	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a character-by-character secret reached the wire: %q", got)
	}
	if want := "before [redacted] after"; got != want {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

// TestASecretWhollyInsideOneFragmentIsStillRedacted guards the behaviour the
// streaming redactor replaced. A cross-fragment redactor that lost the simple
// case would be a regression dressed as a fix.
func TestASecretWhollyInsideOneFragmentIsStillRedacted(t *testing.T) {
	withPolicy(t, keyPattern)

	got := pushAll([]string{"all of it: " + antKey + " and then some trailing prose"})

	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a whole-fragment secret reached the wire: %q", got)
	}
}

// TestTheShippedConcatenationDoesNotDependOnWhereTheSplitsFall is the
// property-style statement of the same guarantee: for one input, every way of
// cutting it into fragments must produce the same bytes on the wire, and those
// bytes must be what redacting the whole text produces.
func TestTheShippedConcatenationDoesNotDependOnWhereTheSplitsFall(t *testing.T) {
	withPolicy(t, []string{`xk-tok-[A-Za-z0-9-]{8,64}`, `ZKIA[0-9A-Z]{16}`})

	whole := "intro " + antKey + " middle ZKIAABCDEFGHIJKLMNOP tail"
	want := Text(whole)
	if strings.Contains(want, "xk-tok") || strings.Contains(want, "ZKIA") {
		t.Fatalf("the whole-text redaction is itself wrong: %q", want)
	}

	// Every single-cut split, then a few multi-cut ones.
	for cut := 0; cut <= len(whole); cut++ {
		got := pushAll([]string{whole[:cut], whole[cut:]})
		if got != want {
			t.Fatalf("split at %d shipped %q, want %q", cut, got, want)
		}
	}
	for _, step := range []int{1, 2, 3, 5, 7, 11, 17} {
		var fragments []string
		for i := 0; i < len(whole); i += step {
			end := i + step
			if end > len(whole) {
				end = len(whole)
			}
			fragments = append(fragments, whole[i:end])
		}
		if got := pushAll(fragments); got != want {
			t.Fatalf("fragments of %d bytes shipped %q, want %q", step, got, want)
		}
	}
}

// TestASecretStraddlingTheHoldBackBoundaryIsRedacted covers the second rule the
// window needs. A buffer longer than the window has a cut point, and a COMPLETE
// match can sit across it - the opening half would ship unredacted and the rest
// would be held, so the secret arrives in two clean pieces. The cut must move
// back to the match's start instead.
func TestASecretStraddlingTheHoldBackBoundaryIsRedacted(t *testing.T) {
	withPolicy(t, keyPattern)

	// Place the key so that it spans the offset the window alone would cut at.
	// The tail must start OUTSIDE the key's character class, or the greedy
	// pattern swallows it and the test measures its own construction.
	tail := " " + strings.Repeat("z", StreamHoldBack-len(antKey)/2)
	head := strings.Repeat("y", 200) + " "
	whole := head + antKey + tail

	got := pushAll([]string{whole})

	if strings.Contains(got, "xk-tok") {
		t.Fatalf("a secret straddling the hold-back boundary shipped its opening "+
			"bytes unredacted: %q", got)
	}
	if want := head + "[redacted]" + tail; got != want {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

// TestNoTextIsLost pins the other half of the trade. A hold-back that forgets
// to flush is not a safety win, it is silent data loss - and the reader cannot
// tell the difference between prose that was withheld and prose that never
// existed.
func TestNoTextIsLost(t *testing.T) {
	withPolicy(t, keyPattern)

	whole := strings.Repeat("ordinary prose with no secrets at all. ", 40)
	fragments := []string{}
	for i := 0; i < len(whole); i += 13 {
		end := i + 13
		if end > len(whole) {
			end = len(whole)
		}
		fragments = append(fragments, whole[i:end])
	}

	if got := pushAll(fragments); got != whole {
		t.Fatalf("streaming %d bytes shipped %d - text was lost or duplicated",
			len(whole), len(got))
	}
}

// TestATailIsHeldBackUntilItIsFlushed states the cost of the trade explicitly,
// so a future change that "optimises" the hold away has to argue with a test.
// The cost is now paid only by text that could still be the start of a match:
// the fragment ends in the first bytes of the key shape, and those bytes wait
// for the flush while the prose ahead of them ships at once.
func TestATailIsHeldBackUntilItIsFlushed(t *testing.T) {
	withPolicy(t, keyPattern)

	var s Stream
	if got := s.Push("short fragment xk-t"); got != "short fragment " {
		t.Errorf("Push shipped %q, want the prose and not the opening bytes of "+
			"a key - a later fragment could still complete a match across them", got)
	}
	if !s.Pending() {
		t.Fatal("Pending() = false while a tail is held")
	}
	if got := s.Flush(); got != "xk-t" {
		t.Errorf("Flush shipped %q, want the held tail", got)
	}
	if s.Pending() {
		t.Error("Pending() = true after Flush")
	}
}

// TestWithNoPolicyEveryFragmentShipsImmediately keeps the liveness of the
// unconfigured workspace, which is the common case: a workspace that redacts
// nothing must not pay a hold-back window for it.
func TestWithNoPolicyEveryFragmentShipsImmediately(t *testing.T) {
	old := Current()
	SetPolicy(nil)
	t.Cleanup(func() { SetPolicy(old) })

	var s Stream
	if got := s.Push("live text"); got != "live text" {
		t.Errorf("Push shipped %q, want the fragment unchanged and immediately", got)
	}
	if s.Pending() {
		t.Error("a stream with no policy is holding text back")
	}
}

// TestAMatchLongerThanTheOldWindowIsHeldWhole closes the hole the flat window
// had. A pattern matching more than StreamHoldBack bytes could begin further
// back than the window reached, and its opening bytes were on the wire before
// the closing bytes proved it was a secret. Under the automaton the header
// opens a live partial match, so every byte from it onward waits for the
// closing bytes: nothing of the body ships before END, and the wire is
// Text(whole). Both a closed long-body pattern and the shipped PEM rule.
func TestAMatchLongerThanTheOldWindowIsHeldWhole(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		whole    string
		body     string
	}{
		{
			name:     "closed pattern",
			patterns: []string{`(?s)BEGIN KEY.*?END KEY`},
			whole:    "prose " + "BEGIN KEY" + strings.Repeat("x", 1600) + "END KEY" + " after",
			body:     "xxxx",
		},
		{
			name:     "shipped PEM rule",
			patterns: shippedPatterns(t),
			whole: "prose " + "-----BEGIN TEST PRIVATE KEY-----" +
				strings.Repeat("\nMIIBogIBAAJBAK", 110) + "\n-----END TEST PRIVATE KEY-----" + " after",
			body: "MIIB",
		},
	}
	for _, tc := range cases {
		withPolicy(t, tc.patterns)
		want := Text(tc.whole)
		if strings.Contains(want, tc.body) {
			t.Fatalf("%s: the whole-text redaction failed, so this test proves nothing: %q", tc.name, want)
		}
		for _, size := range []int{1, 4, 17, 300, 356, 1000} {
			var fragments []string
			for i := 0; i < len(tc.whole); i += size {
				fragments = append(fragments, tc.whole[i:min(i+size, len(tc.whole))])
			}
			var s Stream
			var shipped strings.Builder
			for _, f := range fragments {
				shipped.WriteString(s.Push(f))
				if strings.Contains(shipped.String(), tc.body) {
					t.Fatalf("%s, %d-byte fragments: body bytes shipped in the clear "+
						"before the match closed: %q", tc.name, size, shipped.String())
				}
			}
			shipped.WriteString(s.Flush())
			if got := shipped.String(); got != want {
				t.Fatalf("%s, %d-byte fragments: shipped %q, want %q", tc.name, size, got, want)
			}
		}
	}
}
