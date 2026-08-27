package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzStripHTMLTags checks three properties of the pure HTML-stripping
// function:
//
//  1. it never panics on arbitrary input,
//  2. its output is always valid UTF-8,
//  3. text containing no '<', no '&', and no whitespace passes through
//     byte-identically (whitespace normalization is a separate, intentional
//     behavior; and idempotence is false — "&lt;b&gt;" → "<b>" → "bold" — so
//     it is not asserted).
//
// Seeds are drawn from the entity and tag corpus in the regression tests.
func FuzzStripHTMLTags(f *testing.F) {
	seeds := []string{
		"",
		"plain",
		"plain-text-123",
		"plain text with spaces",
		"&",
		"&#",
		"&#x;",
		"&#;",
		"&amp;",
		"&lt;b&gt;bold&lt;/b&gt;",
		"AT&T and more",
		"x &y &z",
		"A&B < 5",
		"A &#x41; B",
		"a <b>bold</b> c",
		"<p>para</p>",
		"&#x1F600;",
		"&#xZZ;",
		"&#0;",
		"&#-1;",
		"&#x110000;",
		"&#1114112;",
		"&#99999999999999999999;",
		"&amp;&amp;",
		"&amp; T",
		"<div>block</div> tail",
		"<li>line 1</li>",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out := stripHTMLTags(input)
		if !utf8.ValidString(out) {
			t.Fatalf("stripHTMLTags(%q) produced invalid UTF-8: %q", input, out)
		}
		if utf8.ValidString(input) && !strings.ContainsAny(input, "<&") && !strings.ContainsAny(input, " \t\n\r\f") {
			if out != input {
				t.Fatalf("stripHTMLTags(%q) = %q, want byte-identical passthrough", input, out)
			}
		}
	})
}
