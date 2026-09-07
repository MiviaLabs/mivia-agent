package memory

import (
	"strings"
	"testing"
)

// A hand-authored protocol memory whose body opens with an "# " heading but
// uses section names Parse does not know must keep its body text: the
// structural re-parse is an optimization for files RenderProtocolFile wrote,
// never a licence to discard the body of a file it did not.
func TestParseProtocolMemoryKeepsNonTemplateBody(t *testing.T) {
	const file = `---
id: dispatch-protocol
title: 'Dispatch protocol'
content: 'One-line summary.'
importance: high
updated: 2026-01-02
tags: [dispatch, protocol]
x-verdict: good
---

# Dispatch protocol

## Context & Pitfalls

A dispatch that saturates max_workers stalls behind an HTTP 503 retry.

## Required Practice

Bound the wave and assert on the ledger.
`

	e, id, ok := parseProtocolMemory([]byte(file), ScopeProject)
	if !ok {
		t.Fatalf("parseProtocolMemory rejected a well-formed protocol file")
	}
	if id != "dispatch-protocol" {
		t.Fatalf("id = %q, want dispatch-protocol", id)
	}
	for _, want := range []string{"max_workers", "HTTP 503", "Required Practice"} {
		if !strings.Contains(e.Why, want) {
			t.Fatalf("body text %q lost from Why; Why = %q", want, e.Why)
		}
	}
	if e.Summary != "One-line summary." {
		t.Fatalf("Summary = %q, want the frontmatter content", e.Summary)
	}
}

// The template shape RenderProtocolFile writes must still round-trip into the
// dedicated fields rather than collapsing into Why.
func TestParseProtocolMemoryRoundTripsTemplateBody(t *testing.T) {
	e := Entry{
		Title: "Round trip", Scope: ScopeProject, Verdict: VerdictGood,
		Importance: ImportanceHigh, Summary: "Summary line.",
		Good: "Worked well.", Bad: "Did not work.", Why: "Because of the seam.",
		References: []string{"internal/memory/entry.go"},
		Tags:       []string{"memory"}, Created: "2026-01-02",
	}
	rendered := e.RenderProtocolFile("round-trip")

	got, _, ok := parseProtocolMemory([]byte(rendered), ScopeProject)
	if !ok {
		t.Fatalf("parseProtocolMemory rejected its own RenderProtocolFile output")
	}
	if got.Good != e.Good || got.Bad != e.Bad || got.Why != e.Why {
		t.Fatalf("template round-trip lost fields: good=%q bad=%q why=%q", got.Good, got.Bad, got.Why)
	}
	if len(got.References) != 1 || got.References[0] != e.References[0] {
		t.Fatalf("references = %v, want %v", got.References, e.References)
	}
}

// protocolFile wraps a body in the minimal protocol frontmatter.
func protocolFile(body string) string {
	return "---\nid: partial\ntitle: 'Partial'\ncontent: 'Summary line.'\nimportance: high\nupdated: 2026-01-02\ntags: [ops]\nx-verdict: good\n---\n\n" + body
}

// A body that mixes ONE recognized template section with unrecognized
// sections and free text is still hand-authored: the structural parse cannot
// account for it, so adopting the parse would silently drop everything it
// does not recognize. Only a body the parser fully accounts for may be
// adopted.
func TestParseProtocolMemoryKeepsPartialTemplateBody(t *testing.T) {
	body := "# Incident\n\nnote: capture this\n\n## Why\nbecause\n\n## Follow-ups\n- email Bob\n"

	e, _, ok := parseProtocolMemory([]byte(protocolFile(body)), ScopeProject)
	if !ok {
		t.Fatal("parseProtocolMemory rejected a well-formed protocol file")
	}
	for _, want := range []string{"capture this", "Follow-ups", "email Bob", "because"} {
		if !strings.Contains(e.Why, want) {
			t.Fatalf("body text %q lost; Why = %q", want, e.Why)
		}
	}
}

// The regression this must never reproduce: a body with a recognized section
// but no "## Why" had Why overwritten with the parse's empty Why, losing the
// body outright - strictly worse than the pre-fix behavior.
func TestParseProtocolMemoryNeverBlanksWhy(t *testing.T) {
	body := "# T\n\n## What worked\nstuff\n\n## Notes\nkeep me\n"

	e, _, ok := parseProtocolMemory([]byte(protocolFile(body)), ScopeProject)
	if !ok {
		t.Fatal("parseProtocolMemory rejected a well-formed protocol file")
	}
	if e.Why == "" {
		t.Fatal("Why was blanked by the structural re-parse; the body is lost")
	}
	if !strings.Contains(e.Why, "keep me") || !strings.Contains(e.Why, "stuff") {
		t.Fatalf("body text lost; Why = %q", e.Why)
	}
}

// A body made ENTIRELY of recognized sections is fully accounted for, so it
// still round-trips into the dedicated fields and must not collapse into Why.
func TestParseProtocolMemoryAdoptsFullyRecognizedBody(t *testing.T) {
	body := "# T\n\n## Summary\nsum\n\n## What worked\ngood\n\n## Why\nbecause\n"

	e, _, ok := parseProtocolMemory([]byte(protocolFile(body)), ScopeProject)
	if !ok {
		t.Fatal("parseProtocolMemory rejected a well-formed protocol file")
	}
	if e.Why != "because" {
		t.Fatalf("Why = %q, want the parsed section", e.Why)
	}
	if e.Good != "good" {
		t.Fatalf("Good = %q, want the parsed section", e.Good)
	}
}

// RenderProtocolFile writes tags into an unquoted YAML flow sequence, so
// Validate must refuse any tag the repo's own memories gate
// (scripts/check_memories.py) would reject in that position.
func TestValidateRejectsGateHostileTags(t *testing.T) {
	base := Entry{
		Title: "Gate hostile", Scope: ScopeProject, Verdict: VerdictGood,
		Importance: ImportanceLow, Summary: "Summary.", Why: "Why.",
		Created: "2026-01-02",
	}
	for _, tag := range []string{"ci:deploy", "a]b", "a[b", "a{b", "a}b", "a #c"} {
		e := base
		e.Tags = []string{tag}
		if err := e.Validate(Limits{}); err == nil {
			t.Fatalf("Validate accepted gate-hostile tag %q", tag)
		}
	}
	ok := base
	ok.Tags = []string{"ci-deploy", "memory"}
	if err := ok.Validate(Limits{}); err != nil {
		t.Fatalf("Validate rejected plain keyword tags: %v", err)
	}
}
