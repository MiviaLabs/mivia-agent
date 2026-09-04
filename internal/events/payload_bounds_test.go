package events

import (
	"strings"
	"testing"
)

// TestCompactionEventRejectsOverlongReason pins the Reason length bound.
// Reason is free-form operator text that rides a progress event onto every
// subscriber and the durable ledger; unbounded it turns a content-free
// payload into an arbitrary-size channel. The constructor must refuse it
// and name the field so the operator knows which one to shorten.
func TestCompactionEventRejectsOverlongReason(t *testing.T) {
	params := CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 10, AfterTokens: 5,
		SourceRange: compactionTestRange(t), SummaryVersion: 1,
		Reason: strings.Repeat("r", 257),
	}
	_, err := NewCompactionEvent(params)
	if err == nil {
		t.Fatal("a 257-byte reason was accepted")
	}
	if got, want := err.Error(), "invalid compaction event: reason"; got != want {
		t.Fatalf("error = %q; want %q", got, want)
	}
	// 256 is the boundary and must still be accepted, so the bound is a
	// limit rather than an off-by-one that rejects legitimate text.
	params.Reason = strings.Repeat("r", 256)
	if _, err := NewCompactionEvent(params); err != nil {
		t.Fatalf("a 256-byte reason was refused: %v", err)
	}
}

// TestCacheUsageEventRejectsUnusableStyle pins the Style bound. Style is
// the attribution that tells an operator which cache path produced the
// counts; blank or overlong it is either unattributable or an unbounded
// string on a payload that is meant to carry counts only.
func TestCacheUsageEventRejectsUnusableStyle(t *testing.T) {
	const want = "invalid cache usage event: style"
	for _, style := range []string{"", "   ", "\t", strings.Repeat("s", 33)} {
		_, err := NewCacheUsageEvent("deepseek", "deepseek-v4-pro", style, 100, 80, 0)
		if err == nil {
			t.Fatalf("style %q was accepted", style)
		}
		if err.Error() != want {
			t.Fatalf("style %q gave error %q; want %q", style, err.Error(), want)
		}
	}
	if _, err := NewCacheUsageEvent("deepseek", "deepseek-v4-pro", strings.Repeat("s", 32), 1, 0, 0); err != nil {
		t.Fatalf("a 32-byte style was refused: %v", err)
	}
}
