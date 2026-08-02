package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestCompactionEventIsSealedAndContentFree(t *testing.T) {
	rangeValue := compactionTestRange(t)
	event, err := NewCompactionEvent(CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 82, AfterTokens: 49,
		ElidedMessages: 3, ElidedBytes: 12000, SourceRange: rangeValue, SummaryVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"\"content\"", "\"input\"", "\"output\"", "secret-sentinel", "read_file", "sha256"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("typed event contains forbidden field/value %q: %s", forbidden, encoded)
		}
	}
	if event.Trigger != "threshold" || event.BeforeTokens != 82 || event.AfterTokens != 49 {
		t.Fatalf("event fields = %+v", event)
	}
	if event.ElidedMessages != 3 || event.ElidedBytes != 12000 {
		t.Fatalf("elision fields = %+v", event)
	}
}

func TestCompactionEventRejectsNegativeElisionCounters(t *testing.T) {
	rangeValue := compactionTestRange(t)
	if _, err := NewCompactionEvent(CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 10, AfterTokens: 5,
		ElidedMessages: -1, SourceRange: rangeValue, SummaryVersion: 1,
	}); err == nil {
		t.Fatal("negative ElidedMessages accepted")
	}
	if _, err := NewCompactionEvent(CompactionEventParams{
		Trigger: "threshold", BeforeTokens: 10, AfterTokens: 5,
		ElidedBytes: -1, SourceRange: rangeValue, SummaryVersion: 1,
	}); err == nil {
		t.Fatal("negative ElidedBytes accepted")
	}
}

func compactionTestRange(t *testing.T) contextstate.SourceRange {
	t.Helper()
	id := contextstate.SourceID{SessionID: "event-session", Sequence: 2}
	rangeValue, err := contextstate.NewSourceRange(id, id)
	if err != nil {
		t.Fatal(err)
	}
	return rangeValue
}
