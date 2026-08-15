package hub

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestWireCompactionRoundTripsSummarized pins that the cross-process
// projection preserves whether a compaction actually summarized. The field
// defaults to false, so dropping it does not merely lose information - it
// makes every relayed compaction assert "structural only, no summary",
// including summarized ones. A second surface would render the opposite of
// what happened.
func TestWireCompactionRoundTripsSummarized(t *testing.T) {
	for _, summarized := range []bool{true, false} {
		typed, err := events.NewCompactionEvent(events.CompactionEventParams{
			Trigger: "threshold", BeforeTokens: 900, AfterTokens: 400,
			SourceRange: contextstate.SourceRange{
				Start: contextstate.SourceID{SessionID: "s", Sequence: 1},
				End:   contextstate.SourceID{SessionID: "s", Sequence: 2},
			},
			SummaryVersion: 1, Summarized: summarized,
		})
		if err != nil {
			t.Fatal(err)
		}
		ev := events.NewEvent(events.KindCompaction)
		ev.Compaction = &typed

		back := fromWire(toWire(ev))
		if back.Compaction == nil {
			t.Fatal("compaction payload lost in the round trip")
		}
		if back.Compaction.Summarized != summarized {
			t.Fatalf("summarized round-tripped as %v, want %v",
				back.Compaction.Summarized, summarized)
		}
	}
}
