package redact

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The latency half of the trade.
//
// The hold-back was a flat 256 bytes. A model streaming four bytes a delta at
// 140 B/s put the viewer two seconds behind at full speed and an unbounded
// time behind during every pause, while tool events - which never pass through
// the hold - rendered at once. These tests pin the repair: the held tail is
// only what could still be the beginning of a match under the SHIPPED policy,
// and every safety test in stream_test.go still holds.

// shippedPatterns reads redaction_patterns from .mivia/mivia.toml.example, so
// the bound below is measured against the rules an operator actually installs.
func shippedPatterns(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".mivia", "mivia.toml.example"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	_, rest, found := strings.Cut(string(data), "redaction_patterns = [")
	if !found {
		t.Fatal("no redaction_patterns array in the example config")
	}
	var patterns []string
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if line == "]" {
			break
		}
		if strings.HasPrefix(line, "'") {
			patterns = append(patterns, strings.TrimSuffix(strings.Trim(line, "',"), "'"))
		}
	}
	if len(patterns) < 4 {
		t.Fatalf("parsed %d shipped patterns, want at least 4: %q", len(patterns), patterns)
	}
	return patterns
}

// longestOpener is the most bytes of ordinary prose the shipped rules can keep
// open: the longest key-name word plus the optional quote before it.
const longestOpener = len(`"authorization`)

// prose is ordinary answer text: no key names, no vendor prefixes.
const prose = "Here is what I found. The build runs in about four minutes on the " +
	"shared runner, and most of that is the integration suite booting its " +
	"container. Moving the slow cases behind a flag would halve the wall time " +
	"without changing what the gate proves. I would start there."

// fourByteDeltas splits text the way a token-by-token provider streams it.
func fourByteDeltas(text string) []string {
	var out []string
	for i := 0; i < len(text); i += 4 {
		end := min(i+4, len(text))
		out = append(out, text[i:end])
	}
	return out
}

// TestOrdinaryProseTrailsProductionByAtMostOneOpener is the discriminator for
// the lag itself. A pause has no clock here - the Stream is pure - so the
// bound is asserted after EVERY push: whatever the viewer would see during a
// 500ms silence after that push is exactly what has shipped by then. On the
// flat window this trails by 256 bytes at the first push that clears it.
func TestOrdinaryProseTrailsProductionByAtMostOneOpener(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	var s Stream
	var shipped strings.Builder
	pushed, worst := 0, 0
	for _, d := range fourByteDeltas(prose) {
		shipped.WriteString(s.Push(d))
		pushed += len(d)
		if lag := pushed - shipped.Len(); lag > worst {
			worst = lag
		}
	}
	shipped.WriteString(s.Flush())
	t.Logf("worst trailing bytes over %d four-byte deltas: %d (bound %d)",
		len(fourByteDeltas(prose)), worst, longestOpener)
	if worst > longestOpener {
		t.Fatalf("the shipped text trailed production by %d bytes, want at most "+
			"%d - the viewer sits that far behind the model during every pause",
			worst, longestOpener)
	}
	if got := shipped.String(); got != prose {
		t.Fatalf("shipped %q, want the prose unchanged", got)
	}
}

// TestAnAnchorArrivingOneCharacterPerDeltaIsStillRedacted: the vendor prefix
// is exactly the shape the content-aware hold has to keep, and a token-by-token
// provider delivers it one byte at a time. The key is assembled here so the
// repo's secret scanner does not read a fixture as a credential.
func TestAnAnchorArrivingOneCharacterPerDeltaIsStillRedacted(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	key := "sk-" + "ant-" + "api03-AAAABBBB"
	whole := "use " + key + " for it"
	var fragments []string
	for _, r := range whole {
		fragments = append(fragments, string(r))
	}

	got := pushAll(fragments)
	if strings.Contains(got, "sk-") {
		t.Fatalf("a character-by-character key reached the wire: %q", got)
	}
	if want := Text(whole); got != want {
		t.Errorf("shipped %q, want %q", got, want)
	}
}

// TestAKeyNameSplitFromItsValueIsStillRedacted is the case a literal-anchor
// hold gets wrong. `token: abc` matches the shipped key-name rule, and a model
// emits it as three deltas: after `token` and `: ` the anchor is complete and
// the value absent, so a hold keyed on the anchor's prefix ships `token: ` and
// the value then arrives alone and matches nothing. The hold must follow the
// pattern, not its first word.
func TestAKeyNameSplitFromItsValueIsStillRedacted(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	for _, fragments := range [][]string{
		{"the ", "token", ": ", "abc12", " is set"},
		{"send ", "Bearer", " ", "abc.def", " with it"},
		{"the ", "api", "_", "key", " = ", "q1w2e3", " here"},
	} {
		whole := strings.Join(fragments, "")
		want := Text(whole)
		if want == whole {
			t.Fatalf("the whole-text redaction leaves %q alone, so the split proves nothing", whole)
		}
		if got := pushAll(fragments); got != want {
			t.Errorf("fragments %q shipped %q, want %q", fragments, got, want)
		}
	}
}

// TestProseThatMerelyMentionsAKeyNameDoesNotStall: "the token: is fine here"
// IS redacted by the shipped rule - whole-text and streamed alike, which is the
// pre-existing behaviour - but it must not pin the stream. Once the value's
// terminator arrives the match is closed and the rest ships on time.
func TestProseThatMerelyMentionsAKeyNameDoesNotStall(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	whole := "the token: is fine here, and the secret of it is that nothing " +
		"after the password matters to the reader at all."
	var s Stream
	var shipped strings.Builder
	for _, d := range fourByteDeltas(whole) {
		shipped.WriteString(s.Push(d))
	}
	if held := len(s.held); held > longestOpener {
		t.Fatalf("the stream is holding %d bytes before the flush; a mention of "+
			"a key name pinned it", held)
	}
	shipped.WriteString(s.Flush())
	if got, want := shipped.String(), Text(whole); got != want {
		t.Fatalf("shipped %q, want the whole-text redaction %q", got, want)
	}
}

// TestAnOpenEndedPEMRulePinsAtItsHeader keeps the documented behaviour of the
// shipped `|$` alternative: once the header is in the buffer every later byte
// is a live match, so nothing ships until the block closes - the safe
// direction, and the one the wire doc tells operators about.
func TestAnOpenEndedPEMRulePinsAtItsHeader(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	header := "-----BEGIN TEST PRIVATE KEY-----"
	whole := "before " + header + strings.Repeat("\nMIIBogIBAAJBAK", 40) + "\nand prose after"
	var s Stream
	var shipped strings.Builder
	for _, d := range fourByteDeltas(whole) {
		shipped.WriteString(s.Push(d))
	}
	if got := shipped.String(); got != "before " {
		t.Fatalf("shipped %q before the flush, want only the text ahead of the header", got)
	}
	shipped.WriteString(s.Flush())
	if got, want := shipped.String(), Text(whole); got != want || strings.Contains(got, "MIIB") {
		t.Fatalf("shipped %q, want %q", got, want)
	}
}

// TestRandomSplitsOfRandomProseShipTheWholeTextRedaction is the property the
// others are instances of: for random prose with secrets sprinkled in, cut at
// random points, the wire carries exactly Text(whole).
func TestRandomSplitsOfRandomProseShipTheWholeTextRedaction(t *testing.T) {
	withPolicy(t, shippedPatterns(t))

	words := strings.Fields(prose + " token secret: api_key=v1 Bearer x9 \"password\": pw1 sk-" +
		"ant-k1k2k3 ghp_" + "abcdefgh xoxb-1-2 -----BEGIN é ü 日本語")
	rng := rand.New(rand.NewPCG(7, 11))
	for round := 0; round < 400; round++ {
		var b strings.Builder
		for i, n := 0, 3+rng.IntN(40); i < n; i++ {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteString([]string{" ", " ", ", ", "\n", ""}[rng.IntN(5)])
		}
		whole := b.String()
		var fragments []string
		for i := 0; i < len(whole); {
			end := min(i+1+rng.IntN(9), len(whole))
			fragments = append(fragments, whole[i:end])
			i = end
		}
		if got, want := pushAll(fragments), Text(whole); got != want {
			t.Fatalf("round %d: fragments %q shipped\n%q\nwant\n%q", round, fragments, got, want)
		}
	}
}
