package jschema_test

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func TestExtractEnvelope_HappyPath(t *testing.T) {
	in := "<mivia_output>\n{\"ok\":true}\n</mivia_output>"
	if got := jschema.ExtractEnvelope(in); got != `{"ok":true}` {
		t.Fatalf("ExtractEnvelope(%q) = %q", in, got)
	}
}

func TestExtractEnvelope_TrimsPreambleAndPostamble(t *testing.T) {
	in := "Sure, here you go:\n<mivia_output>\n{\"ok\":true}\n</mivia_output>\nThanks!"
	if got := jschema.ExtractEnvelope(in); got != `{"ok":true}` {
		t.Fatalf("ExtractEnvelope(%q) = %q", in, got)
	}
}

// TestExtractEnvelope_IgnoresTagMentionedInProse pins the fix for the
// confirmed extraction bug found in Step 0 review: a model narrating
// compliance ("I'll wrap this in <mivia_output> tags:") before the real
// envelope must not have that mention mistaken for the real opening tag. The
// mentioned tag has trailing prose on its line ("tags:"), so it is not
// line-bound and must be skipped in favor of the real, line-bound tag.
func TestExtractEnvelope_IgnoresTagMentionedInProse(t *testing.T) {
	in := "I'll wrap this in <mivia_output> tags:\n<mivia_output>\n{\"ok\":true}\n</mivia_output>"
	if got := jschema.ExtractEnvelope(in); got != `{"ok":true}` {
		t.Fatalf("ExtractEnvelope(%q) = %q, want the real envelope content, not the prose mention", in, got)
	}
}

// TestExtractEnvelope_UsesLastCloseTagWhenPayloadContainsLiteralTag pins the
// other Step 0 finding: if the JSON payload itself contains a line that looks
// like a closing tag, the LAST line-bound close tag must win so the real
// closing tag (further down) is not truncated away.
func TestExtractEnvelope_UsesLastCloseTagWhenPayloadContainsLiteralTag(t *testing.T) {
	in := "<mivia_output>\n{\"note\":\"see\n</mivia_output>\nhere\"}\n</mivia_output>"
	want := "{\"note\":\"see\n</mivia_output>\nhere\"}"
	if got := jschema.ExtractEnvelope(in); got != want {
		t.Fatalf("ExtractEnvelope(%q) = %q, want %q", in, got, want)
	}
}

func TestExtractEnvelope_FallsBackWhenNoTag(t *testing.T) {
	in := `{"ok":true}`
	if got := jschema.ExtractEnvelope(in); got != in {
		t.Fatalf("ExtractEnvelope(%q) = %q, want unchanged input", in, got)
	}
}

func TestExtractEnvelope_FallsBackWhenUnclosed(t *testing.T) {
	in := "<mivia_output>\n{\"ok\":true}"
	if got := jschema.ExtractEnvelope(in); got != in {
		t.Fatalf("ExtractEnvelope(%q) = %q, want unchanged input", in, got)
	}
}

func TestEnvelopeAppendixBody_ContainsTagAndContract(t *testing.T) {
	body := jschema.EnvelopeAppendixBody("CONTRACT-TEXT")
	if !strings.Contains(body, "<mivia_output>") || !strings.Contains(body, "</mivia_output>") {
		t.Fatalf("EnvelopeAppendixBody missing envelope tag: %q", body)
	}
	if !strings.Contains(body, "CONTRACT-TEXT") {
		t.Fatalf("EnvelopeAppendixBody must carry the contract: %q", body)
	}
}

func TestExtractOutputCandidate_ComposesEnvelopeThenFence(t *testing.T) {
	in := "<mivia_output>\n```json\n{\"ok\":true}\n```\n</mivia_output>"
	if got := jschema.ExtractOutputCandidate(in); strings.TrimSpace(got) != `{"ok":true}` {
		t.Fatalf("ExtractOutputCandidate(%q) = %q", in, got)
	}
}

// TestExtractEnvelope_LeadingProseGluedToTagFallsBack isolates the
// leading-prose half of the line-bound guard: the tag is glued directly to
// preceding prose with no newline, but is otherwise alone on the rest of its
// line. Step 5 bug-audit found that the only existing "tag mentioned in
// prose" fixture fails both the leading- and trailing-whitespace checks at
// once, so a regression that silently dropped just the leading check would
// not have been caught.
func TestExtractEnvelope_LeadingProseGluedToTagFallsBack(t *testing.T) {
	in := "Answer:<mivia_output>\n{\"ok\":true}\n</mivia_output>"
	if got := jschema.ExtractEnvelope(in); got != in {
		t.Fatalf("ExtractEnvelope(%q) = %q, want unchanged input (tag is not alone on its line)", in, got)
	}
}

// TestExtractEnvelope_TrailingProseGluedToTagFallsBack is the mirror of
// TestExtractEnvelope_LeadingProseGluedToTagFallsBack, isolating the
// trailing-prose half of the guard.
func TestExtractEnvelope_TrailingProseGluedToTagFallsBack(t *testing.T) {
	in := "<mivia_output>\n{\"ok\":true}\n</mivia_output> done"
	if got := jschema.ExtractEnvelope(in); got != in {
		t.Fatalf("ExtractEnvelope(%q) = %q, want unchanged input (tag is not alone on its line)", in, got)
	}
}

// TestExtractEnvelope_ManyRepeatedTagsOnOneLineStaysLinear is the Step 5
// bug-audit regression test: a naive line-bound scan that re-derives each
// candidate's line start by scanning back over the whole prefix (instead of
// walking the string once) is quadratic on a reply that repeats the tag text
// many times on a single unterminated line - a shape a real (or adversarial)
// model reply can produce. This must complete quickly regardless of size.
func TestExtractEnvelope_ManyRepeatedTagsOnOneLineStaysLinear(t *testing.T) {
	in := strings.Repeat("<mivia_output>x", 200000)
	done := make(chan string, 1)
	go func() { done <- jschema.ExtractEnvelope(in) }()
	select {
	case got := <-done:
		if got != in {
			t.Fatalf("ExtractEnvelope of a no-tag-pair adversarial line = %q, want unchanged input", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExtractEnvelope did not return within 2s on a large repeated-tag input (quadratic scan?)")
	}
}
