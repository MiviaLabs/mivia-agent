package events

import (
	"strings"
	"testing"
)

func TestCompactionEventSerializationRejectsGenericEnvelope(t *testing.T) {
	rangeValue := compactionTestRange(t)
	sealed, err := NewCompactionEvent(CompactionEventParams{
		Trigger: "mandatory", BeforeTokens: 100, AfterTokens: 70,
		ElidedMessages: 2, ElidedBytes: 5000, SourceRange: rangeValue, SummaryVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalCompactionEvent(sealed)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, "summary-content") || strings.Contains(encoded, "\"content\"") {
		t.Fatalf("compaction wire contains generic content: %s", raw)
	}
	restored, err := UnmarshalCompactionEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Trigger != sealed.Trigger || restored.SourceRange != sealed.SourceRange {
		t.Fatalf("round trip = %+v", restored)
	}
	if restored.ElidedMessages != 2 || restored.ElidedBytes != 5000 {
		t.Fatalf("elision round trip = msgs=%d bytes=%d", restored.ElidedMessages, restored.ElidedBytes)
	}
	if _, err := MarshalCompactionEvent(CompactionEvent{Trigger: "threshold", BeforeTokens: 2, AfterTokens: 1, SourceRange: rangeValue, SummaryVersion: 1}); err == nil {
		t.Fatal("unsealed generic event was accepted")
	}
}
