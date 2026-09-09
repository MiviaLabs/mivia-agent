package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// seqOrderCheck asserts the wire contract one Project result must keep: the
// Seqs ascend strictly, consecutive seqs are contiguous, and wantLast (the
// turn's terminal type) is the last event in the slice. A viewer renders in
// arrival order and keys replay on the seq, so a terminal emitted before the
// blocks it closes shows the turn end above the prose it ended, and a gap or
// descent in the seqs makes the batch a replay hazard at the store.
func seqOrderCheck(t *testing.T, got []WireEvent, wantLast string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("the terminal event produced no wire events")
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Errorf("seqs not ascending at index %d: %d after %d (%v)",
				i, got[i].Seq, got[i-1].Seq, wireTypes(got))
		}
		if got[i].Seq != got[i-1].Seq+1 {
			t.Errorf("seqs not contiguous at index %d: %d after %d (%v)",
				i, got[i].Seq, got[i-1].Seq, wireTypes(got))
		}
	}
	if last := got[len(got)-1]; last.Type != wantLast {
		t.Errorf("last event = %s (seq %d), want the terminal %s last (%v)",
			last.Type, last.Seq, wantLast, wireTypes(got))
	}
}

func wireTypes(evs []WireEvent) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

// streamTurnWithHeldProse drives a realistic turn to the edge of its terminal:
// turn start, a thinking fragment under the hold-back policy, and assistant
// deltas long enough to leave a held tail. The turn's own close therefore owes
// the wire BOTH the flushed assistant tail AND the settled thinking block, in
// that order, before the terminal.
func streamTurnWithHeldProse(t *testing.T, p *Projector) {
	t.Helper()
	p.Project(rootEvent(events.KindTurnStart, "", ""))
	p.Project(rootEvent(events.KindThinking, "final reasoning", ""))
	p.Project(rootEvent(events.KindAssistant, strings.Repeat("a first sentence that is quite long. ", 12), "delta"))
	p.Project(rootEvent(events.KindAssistant, "a short trailing clause", "delta"))
}

// TestEveryProjectResultHasAscendingSeqs pins the closeTurn order invariant:
// the turn's terminal must be BUILT last, so its seq is assigned after the
// flush and settle events that precede it on the wire. Taking the terminal as
// a pre-evaluated slice gave it the earlier seq while the slice placed it
// first, so every close that owed a flush or a settle published a
// non-contiguous, misordered batch.
func TestEveryProjectResultHasAscendingSeqs(t *testing.T) {
	t.Run("turn end", func(t *testing.T) {
		streamPolicy(t)
		p := NewProjector("sess-1", 0, proseOpts())
		streamTurnWithHeldProse(t, p)
		got := p.Project(rootEvent(events.KindTurnEnd, "", "completed"))
		seqOrderCheck(t, got, TypeTurnEnded)
		if len(got) < 3 {
			t.Fatalf("turn end produced %d events, want at least the flush, the settle and the terminal (%v)",
				len(got), wireTypes(got))
		}
	})

	// The incident shape: turn.failed arrives after thinking.message was still
	// owed. The terminal is the turn failed event instead of the turn end.
	t.Run("turn error", func(t *testing.T) {
		streamPolicy(t)
		p := NewProjector("sess-1", 0, proseOpts())
		streamTurnWithHeldProse(t, p)
		got := p.Project(rootEvent(events.KindError, "", "provider exploded"))
		seqOrderCheck(t, got, TypeTurnFailed)
	})
}
