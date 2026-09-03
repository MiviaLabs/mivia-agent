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
func TestATailIsHeldBackUntilItIsFlushed(t *testing.T) {
	withPolicy(t, keyPattern)

	var s Stream
	if got := s.Push("short fragment"); got != "" {
		t.Errorf("Push shipped %q, want nothing - the whole fragment is inside "+
			"the hold-back window and a later fragment could still complete a "+
			"match across it", got)
	}
	if !s.Pending() {
		t.Fatal("Pending() = false while a tail is held")
	}
	if got := s.Flush(); got != "short fragment" {
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

// TestAMatchLongerThanTheWindowEscapes is the residual risk, pinned rather
// than hidden. A pattern that can match more than StreamHoldBack bytes may
// begin further back than the window reaches, and its opening bytes are then
// already on the wire when the closing bytes prove it was a secret.
//
// This test asserts the LIMIT, not desirable behaviour. If a future change
// makes it fail because the window grew or the algorithm improved, update the
// constant's doc comment in the same commit - the operator-facing claim on
// redact.StreamHoldBack is what this pins.
func TestAMatchLongerThanTheWindowEscapes(t *testing.T) {
	withPolicy(t, []string{`(?s)BEGIN KEY.*?END KEY`})

	body := strings.Repeat("x", StreamHoldBack*2)
	whole := "BEGIN KEY" + body + "END KEY"

	if got := Text(whole); strings.Contains(got, "BEGIN KEY") {
		t.Fatalf("the whole-text redaction failed, so this test proves nothing: %q", got)
	}
	// The split has to land far enough in that the match is still INCOMPLETE
	// when the first fragment is processed - that is precisely the hole. A
	// split near the start is caught, because the whole match is present in
	// the buffer by the time anything is eligible to ship.
	cut := StreamHoldBack + 100
	got := pushAll([]string{whole[:cut], whole[cut:]})
	if !strings.Contains(got, "BEGIN KEY") {
		t.Skip("the window now covers this match; update StreamHoldBack's doc comment")
	}
	t.Logf("documented residual risk: a match of %d bytes exceeds the %d-byte "+
		"hold-back window and streams unredacted", len(whole), StreamHoldBack)
}
