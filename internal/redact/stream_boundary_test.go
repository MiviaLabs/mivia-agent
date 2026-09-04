package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// usePolicy installs a redaction policy for the duration of the test.
// Stream reads the process-wide policy through Current(), so a stream
// test has to set it rather than inject one.
func usePolicy(t *testing.T, patterns ...string) *Policy {
	t.Helper()
	p, err := Compile(patterns, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	previous := Current()
	SetPolicy(p)
	t.Cleanup(func() { SetPolicy(previous) })
	return p
}

// The streaming redactor's cut rules.
//
// Stream.Push decides how much of a delta can be shipped NOW. Everything
// it ships is already on the user's screen and cannot be recalled, so a
// cut in the wrong place is not a rendering artefact - it either leaks
// the head of a secret the next chunk would have completed, or splits a
// rune and puts a replacement character into the transcript.

// TestAnEmptyPolicyShipsEverythingImmediately: with nothing to redact
// there is nothing to hold, and holding anyway would add latency to every
// token of a stream that needs none.
func TestAnEmptyPolicyShipsEverythingImmediately(t *testing.T) {
	var p *Policy // the nil policy is the empty one
	const buf = "nothing secret here"
	if got := p.safeCut(buf); got != len(buf) {
		t.Errorf("safeCut = %d, want the whole buffer (%d)", got, len(buf))
	}

	compiled, err := Compile(nil, nil, "[redacted]")
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.safeCut(buf); got != len(buf) {
		t.Errorf("a policy with no patterns held %d bytes back", len(buf)-got)
	}
}

// TestAStreamNeverSplitsARune is the invariant the backoff loop exists
// for: a delta carrying half a multi-byte character is invalid UTF-8 on
// the wire, and a consumer that concatenates only reassembles it after
// one half has already been drawn as a replacement character.
func TestAStreamNeverSplitsARune(t *testing.T) {
	usePolicy(t, `SECRET-[0-9]+`)

	// A body of multi-byte runes, fed one BYTE at a time. Every emission
	// must be valid UTF-8, and the concatenation must equal the input.
	const body = "héllo — wörld ✓ 日本語"
	var s Stream
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		emitted := s.Push(body[i : i+1])
		if !utf8.ValidString(emitted) {
			t.Fatalf("byte %d: emitted invalid UTF-8 %q", i, emitted)
		}
		out.WriteString(emitted)
	}
	out.WriteString(s.Flush())

	if got := out.String(); got != body {
		t.Errorf("stream reassembled to %q, want %q", got, body)
	}
}

// TestDiscardDropsTheHeldTailWithoutEmittingIt: an assistant reset tells
// the consumer to throw the whole block away, so the held tail must go
// with it. Flushing instead would print the fragment of a message the
// user was just told to ignore.
func TestDiscardDropsTheHeldTailWithoutEmittingIt(t *testing.T) {
	usePolicy(t, `SECRET-[0-9]+`)
	var s Stream

	// A partial match: the stream must hold it rather than ship a prefix.
	if got := s.Push("here comes SECRET-"); strings.Contains(got, "SECRET-") {
		t.Fatalf("the head of a partial match was shipped: %q", got)
	}
	if !s.Pending() {
		t.Fatal("precondition: the stream is holding a partial match")
	}

	s.Discard()
	if s.Pending() {
		t.Error("Discard left the tail held")
	}
	if got := s.Flush(); got != "" {
		t.Errorf("Flush after Discard emitted %q, want nothing", got)
	}
}

// TestCompilePartialRefusesAPatternItCannotReason About: the streaming
// automaton is built from the same source as the matcher, and a pattern
// it cannot parse must be refused rather than held by guess - a policy
// that silently skipped the automaton would ship secrets it could not
// see coming.
func TestCompilePartialRefusesAPatternItCannotReasonAbout(t *testing.T) {
	for _, expr := range []string{"(", "[z-a]", `a{2,1}`, `(?P<`} {
		if _, err := compilePartial(expr); err == nil {
			t.Errorf("compilePartial(%q) accepted an unparseable pattern", expr)
		}
	}
	if _, err := compilePartial(`SECRET-[0-9]+`); err != nil {
		t.Errorf("a well-formed pattern was refused: %v", err)
	}
}
