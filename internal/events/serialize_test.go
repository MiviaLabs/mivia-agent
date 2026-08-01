package events

import (
	"strings"
	"testing"
)

func TestCompactionEventSerializationRejectsGenericEnvelope(t *testing.T) {
	rangeValue := compactionTestRange(t)
	sealed, err := NewCompactionEvent("mandatory", 100, 70, rangeValue, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalCompactionEvent(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "summary-content") || strings.Contains(string(raw), "content") {
		t.Fatalf("compaction wire contains generic content: %s", raw)
	}
	restored, err := UnmarshalCompactionEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Trigger != sealed.Trigger || restored.SourceRange != sealed.SourceRange {
		t.Fatalf("round trip = %+v", restored)
	}
	if _, err := MarshalCompactionEvent(CompactionEvent{Trigger: "threshold", BeforeTokens: 2, AfterTokens: 1, SourceRange: rangeValue, SummaryVersion: 1}); err == nil {
		t.Fatal("unsealed generic event was accepted")
	}
}
