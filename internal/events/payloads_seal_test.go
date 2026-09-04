package events

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The constructor seal, and the control-character screens.
//
// These payloads are published on a bus, relayed across processes, and
// rendered into a terminal. The seal is what stops a zero-value struct
// being published as a real event; the control-character screens are what
// stop a trigger or a model name carrying an escape sequence into a
// surface that will draw it.

func validCompaction(t *testing.T) CompactionEventParams {
	t.Helper()
	start, err := contextstate.NewSourceID("session", 1)
	if err != nil {
		t.Fatal(err)
	}
	end, err := contextstate.NewSourceID("session", 4)
	if err != nil {
		t.Fatal(err)
	}
	return CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 1000, AfterTokens: 400,
		ElidedMessages: 6, ElidedBytes: 2048,
		SourceRange:    contextstate.SourceRange{Start: start, End: end},
		SummaryVersion: 1, Summarized: true, Reason: "over budget",
	}
}

// TestAnUnsealedCompactionEventIsInvalid is the trap the seal exists to
// close: a struct literal assembled by a later consumer looks exactly
// like a real event, and would publish values nothing validated.
func TestAnUnsealedCompactionEventIsInvalid(t *testing.T) {
	var raw CompactionEvent
	if err := raw.Validate(); err == nil {
		t.Fatal("a zero-value compaction event validated")
	} else if !strings.Contains(err.Error(), "seal") {
		t.Errorf("error %q does not name the missing seal", err)
	}

	// A hand-assembled event with entirely valid FIELDS is still refused,
	// because the seal is about provenance, not values.
	p := validCompaction(t)
	handmade := CompactionEvent{
		Trigger: p.Trigger, BeforeTokens: p.BeforeTokens, AfterTokens: p.AfterTokens,
		ElidedMessages: p.ElidedMessages, ElidedBytes: p.ElidedBytes,
		SourceRange: p.SourceRange, SummaryVersion: p.SummaryVersion,
		Summarized: p.Summarized, Reason: p.Reason,
	}
	if err := handmade.Validate(); err == nil {
		t.Error("an unsealed event with valid fields validated")
	}
}

// TestRehydrateSealsARelayedEvent: a payload reconstructed from the wire
// was validated by its publisher, and re-deriving the seal locally is the
// only way a relayed event can pass Validate at the far end. Without it
// every cross-process compaction event would be dropped.
func TestRehydrateSealsARelayedEvent(t *testing.T) {
	p := validCompaction(t)
	original, err := NewCompactionEvent(p)
	if err != nil {
		t.Fatalf("NewCompactionEvent: %v", err)
	}

	// Cross a process boundary: only the exported fields survive.
	relayed := CompactionEvent{
		Trigger: original.Trigger, BeforeTokens: original.BeforeTokens,
		AfterTokens: original.AfterTokens, ElidedMessages: original.ElidedMessages,
		ElidedBytes: original.ElidedBytes, SourceRange: original.SourceRange,
		SummaryVersion: original.SummaryVersion, Summarized: original.Summarized,
		Reason: original.Reason,
	}
	if err := relayed.Validate(); err == nil {
		t.Fatal("precondition: an unsealed relay must not validate on its own")
	}
	if err := RehydrateCompactionEvent(relayed).Validate(); err != nil {
		t.Errorf("a rehydrated relay was still refused: %v", err)
	}
}

// TestCompactionEventRefusesControlCharactersInItsProse: trigger and
// reason are rendered. An escape sequence in either would reach a
// terminal that draws it.
func TestCompactionEventRefusesControlCharactersInItsProse(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"trigger", "thresh\x1b[2Jold"},
		{"trigger", "thresh\x00old"},
		{"reason", "over\x1b[31m budget"},
		{"reason", "over\x7f budget"},
	} {
		p := validCompaction(t)
		if tc.field == "trigger" {
			p.Trigger = tc.value
		} else {
			p.Reason = tc.value
		}
		if _, err := NewCompactionEvent(p); err == nil {
			t.Errorf("%s = %q was accepted", tc.field, tc.value)
		} else if !strings.Contains(err.Error(), "control character") {
			t.Errorf("%s = %q gave %q, want it to name the control character", tc.field, tc.value, err)
		}
	}
}

// TestCompactionEventRefusesImpossibleCounters: compaction cannot end
// larger than it started, and no counter can be negative. Either would
// render as a compaction that added tokens.
func TestCompactionEventRefusesImpossibleCounters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(p *CompactionEventParams)
	}{
		{"after exceeds before", func(p *CompactionEventParams) { p.AfterTokens = p.BeforeTokens + 1 }},
		{"negative before", func(p *CompactionEventParams) { p.BeforeTokens = -1 }},
		{"negative after", func(p *CompactionEventParams) { p.AfterTokens = -1 }},
		{"negative elided messages", func(p *CompactionEventParams) { p.ElidedMessages = -1 }},
		{"negative elided bytes", func(p *CompactionEventParams) { p.ElidedBytes = -1 }},
		{"no summary version", func(p *CompactionEventParams) { p.SummaryVersion = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := validCompaction(t)
			tc.mutate(&p)
			if _, err := NewCompactionEvent(p); err == nil {
				t.Error("an impossible compaction event was constructed")
			}
		})
	}
}

// TestCacheUsageEventRefusesControlCharactersInItsIdentifiers: provider,
// model and style are drawn in the status line, so the same screen
// applies to all three.
func TestCacheUsageEventRefusesControlCharactersInItsIdentifiers(t *testing.T) {
	for _, tc := range []struct{ provider, model, style string }{
		{"zai\x1b[2J", "model", "ephemeral"},
		{"zai", "mo\x00del", "ephemeral"},
		{"zai", "model", "eph\x7femeral"},
	} {
		if _, err := NewCacheUsageEvent(tc.provider, tc.model, tc.style, 100, 10, 5); err == nil {
			t.Errorf("provider=%q model=%q style=%q was accepted", tc.provider, tc.model, tc.style)
		} else if !strings.Contains(err.Error(), "control character") {
			t.Errorf("got %q, want it to name the control character", err)
		}
	}

	if _, err := NewCacheUsageEvent("zai", "model", "ephemeral", 100, 10, 5); err != nil {
		t.Errorf("a clean cache usage event was refused: %v", err)
	}
}
