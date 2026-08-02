package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestRenderCompactionNoticeOmitsContent(t *testing.T) {
	rangeValue := contextstate.SourceRange{
		Start: contextstate.SourceID{SessionID: "session", Sequence: 1},
		End:   contextstate.SourceID{SessionID: "session", Sequence: 3},
	}
	event, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 80, AfterTokens: 40,
		ElidedMessages: 1, ElidedBytes: 4096, SourceRange: rangeValue, SummaryVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	notice := renderCompactionNotice(event)
	if !strings.Contains(notice, "80 -> 40") || strings.Contains(notice, "summary") {
		t.Fatalf("notice = %q", notice)
	}
	if !strings.Contains(notice, "1 tool result elided") || !strings.Contains(notice, "4096 bytes") {
		t.Fatalf("notice missing elision counts: %q", notice)
	}
	// Zero elision keeps the base form.
	plain, err := events.NewCompactionEvent(events.CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 80, AfterTokens: 40,
		SourceRange: rangeValue, SummaryVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	plainNotice := renderCompactionNotice(plain)
	if strings.Contains(plainNotice, "elided") {
		t.Fatalf("zero-elision notice included counts: %q", plainNotice)
	}
}
