package clichat

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
)

// typeCounts groups decoded NDJSON lines by type.
func typeCounts(t *testing.T, out string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, l := range decodeNDJSONLines(t, out) {
		counts[l.Type]++
	}
	return counts
}

// TestExternalTurnStartMintsTheTurnFromItsOwnRunID pins the modern path:
// KindTurnStart carries the real turn id (chat.Session.publishTurnStart), so the
// receiver keys the turn on it directly instead of stashing the text in a scalar
// and waiting for some later event to claim it.
func TestExternalTurnStartMintsTheTurnFromItsOwnRunID(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	renderExternalEvent(&buf, state, events.Event{
		Kind: events.KindTurnStart, SessionID: "s1", TurnID: "turn:1", Detail: "what is 2+2",
	})

	lines := decodeNDJSONLines(t, buf.String())
	if len(lines) != 1 || lines[0].Type != "external_turn_start" {
		t.Fatalf("turn_start alone produced %+v, want one external_turn_start", lines)
	}
	if lines[0].RunID != "turn:1" {
		t.Fatalf("RunID = %q, want the turn_start's own id", lines[0].RunID)
	}
	if lines[0].Text != "what is 2+2" {
		t.Fatalf("Text = %q, want the submitted user text", lines[0].Text)
	}

	// The content that follows must attach to that same turn, not open a second.
	renderExternalEvent(&buf, state, events.Event{
		Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "4", Detail: "delta",
	})
	if got := typeCounts(t, buf.String())["external_turn_start"]; got != 1 {
		t.Fatalf("got %d external_turn_start lines, want 1", got)
	}
}

// TestExternalTerminalForAnUnseenRunIsDropped is the loss-tolerance contract,
// and the last thing standing between relayedKinds and turn terminals.
//
// Every hop from the publishing process to this sink is bounded drop-oldest
// (the bus queue, then the connection's outbound queue), so a terminal can
// legitimately be the first event this sink ever sees for a run. Minting a turn
// in order to immediately close it fabricates an empty turn in the consumer's
// transcript. The consumer has no turn open, so there is nothing to close.
func TestExternalTerminalForAnUnseenRunIsDropped(t *testing.T) {
	for _, kind := range []events.Kind{events.KindTurnEnd, events.KindError} {
		t.Run(string(kind), func(t *testing.T) {
			var buf bytes.Buffer
			state := newExternalTurnState()

			renderExternalEvent(&buf, state, events.Event{
				Kind: kind, SessionID: "s1", TurnID: "turn:lost",
				Err: fmt.Errorf("chat turn failed"),
			})

			if buf.Len() != 0 {
				t.Fatalf("a terminal for an unseen run produced output: %q", buf.String())
			}
			if state.known("turn:lost") {
				t.Fatal("a dropped terminal still allocated tracking state for the run")
			}
		})
	}
}

// TestExternalRunIsNotReopenedAfterItsTerminal covers the duplicate/reordered
// tail. The old receiver deleted the run on its terminal, so any later event
// carrying that run id minted a SECOND external_turn_start - a duplicated turn
// in the transcript, with content arriving after done.
func TestExternalRunIsNotReopenedAfterItsTerminal(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: "turn:1", Detail: "hi"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "hello", Detail: "delta"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnEnd, SessionID: "s1", TurnID: "turn:1"})
	// A straggler for the finished run, and a duplicated start.
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "late", Detail: "delta"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: "turn:1", Detail: "hi again"})

	counts := typeCounts(t, buf.String())
	if counts["external_turn_start"] != 1 {
		t.Fatalf("got %d external_turn_start lines for one run, want 1", counts["external_turn_start"])
	}
	if counts["external_done"] != 1 {
		t.Fatalf("got %d external_done lines, want 1", counts["external_done"])
	}
}

// TestExternalTurnStateStaysBounded is the leak gate. The previous state pruned
// on terminal, so it grew without limit whenever terminals did not arrive - and
// terminals were not relayed at all, so it always grew. Relaying them does not
// fix it either: a terminal is precisely what a drop-oldest queue sheds first.
func TestExternalTurnStateStaysBounded(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	const runs = maxTrackedExternalRuns * 3
	for i := range runs {
		id := "turn:" + strconv.Itoa(i)
		renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: id, Detail: "q"})
		renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: id, Content: "a", Detail: "delta"})
		// No terminal, which is the whole point: nothing here retires a run.
	}

	if len(state.runs) > maxTrackedExternalRuns {
		t.Fatalf("tracked %d runs, cap is %d", len(state.runs), maxTrackedExternalRuns)
	}
	if len(state.order) != len(state.runs) {
		t.Fatalf("order (%d) and runs (%d) disagree; eviction left one of them stale",
			len(state.order), len(state.runs))
	}
	// Eviction must be oldest-first, so the most recent run is still tracked.
	if !state.known("turn:" + strconv.Itoa(runs-1)) {
		t.Fatal("the newest run was evicted; eviction is not oldest-first")
	}
	if state.known("turn:0") {
		t.Fatal("the oldest run survived past the cap")
	}
}

// TestExternalTurnStartWithoutARunIDStillBridgesText keeps the compatibility
// path honest: an older peer publishes KindTurnStart with no turn id, and its
// text must still reach the turn the next event opens.
func TestExternalTurnStartWithoutARunIDStillBridgesText(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", Detail: "legacy text"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:9", Content: "reply"})

	lines := decodeNDJSONLines(t, buf.String())
	if len(lines) == 0 || lines[0].Type != "external_turn_start" {
		t.Fatalf("no external_turn_start minted: %+v", lines)
	}
	if lines[0].Text != "legacy text" {
		t.Fatalf("Text = %q, want the bridged legacy text", lines[0].Text)
	}
	if lines[0].RunID != "turn:9" {
		t.Fatalf("RunID = %q, want the id of the run that claimed the text", lines[0].RunID)
	}
}

// TestExternalLossIsReportedOncePerAdvance is what makes the relay's documented
// lossiness actionable rather than a disclaimer.
//
// Every hop between the two processes is bounded drop-oldest, and until the hub
// carried a counter that loss was completely silent at this end: a consumer
// reading external_chunk text could not tell a short answer from a truncated
// one. The report is per ADVANCE, not per event, so a healthy stream stays
// quiet.
func TestExternalLossIsReportedOncePerAdvance(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	// A quiet stream reports nothing at all.
	reportExternalLoss(&buf, state, hub.Receipt{})
	reportExternalLoss(&buf, state, hub.Receipt{})
	if buf.Len() != 0 {
		t.Fatalf("a lossless stream produced output: %q", buf.String())
	}

	reportExternalLoss(&buf, state, hub.Receipt{Dropped: 4})
	// The same total again is not a new loss.
	reportExternalLoss(&buf, state, hub.Receipt{Dropped: 4})
	reportExternalLoss(&buf, state, hub.Receipt{Dropped: 10})

	lines := decodeNDJSONLines(t, buf.String())
	if len(lines) != 2 {
		t.Fatalf("got %d external_dropped lines, want 2 (one per advance): %+v", len(lines), lines)
	}
	for _, l := range lines {
		if l.Type != "external_dropped" {
			t.Fatalf("type = %q, want external_dropped", l.Type)
		}
	}
	if lines[0].Dropped != 4 || lines[0].TotalDropped != 4 {
		t.Fatalf("first report = delta %d total %d, want 4 and 4", lines[0].Dropped, lines[0].TotalDropped)
	}
	// The delta is what was missed SINCE the last report, not the running total.
	if lines[1].Dropped != 6 || lines[1].TotalDropped != 10 {
		t.Fatalf("second report = delta %d total %d, want 6 and 10", lines[1].Dropped, lines[1].TotalDropped)
	}
}

// TestExternalLossIgnoresARegressingCount guards against a counter that goes
// backwards - which happens when a hub owner exits and another process takes
// over, resetting its connections. A negative delta must not be reported as a
// loss, and must not lower the high-water mark either.
func TestExternalLossIgnoresARegressingCount(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	reportExternalLoss(&buf, state, hub.Receipt{Dropped: 9})
	buf.Reset()
	reportExternalLoss(&buf, state, hub.Receipt{Dropped: 2})

	if buf.Len() != 0 {
		t.Fatalf("a regressing count produced a loss report: %q", buf.String())
	}
	if state.dropped != 9 {
		t.Fatalf("high-water mark = %d, want it held at 9", state.dropped)
	}
}

// TestChatHubSinkReportsLoss drives the REAL production sink, not a helper that
// re-spells it. Deleting the loss report from chatHubSink used to leave the
// whole package green, because every loss test called reportExternalLoss
// directly and the test double had its own copy of the sink body.
func TestChatHubSinkReportsLoss(t *testing.T) {
	sess := newHubTestSession(t, "s1")
	sink, out := newBufSink(sess)

	sink(events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: "turn:1", Detail: "hi"}, hub.Receipt{})
	sink(events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "a", Detail: "delta"}, hub.Receipt{Dropped: 6})

	counts := typeCounts(t, out.String())
	if counts["external_dropped"] != 1 {
		t.Fatalf("got %d external_dropped lines from the production sink, want 1: %s", counts["external_dropped"], out.String())
	}
}

// TestExternalEvictionKeepsALiveRun pins that eviction is least-recently-USED,
// not first-seen. A turn that streams for minutes on a shared workspace ages
// out under a first-seen order even while it is live; the evicted run is then
// re-minted as a SECOND external_turn_start with empty text, and its terminal
// is discarded by the unknown-run guard - a duplicated turn that never closes.
func TestExternalEvictionKeepsALiveRun(t *testing.T) {
	var buf bytes.Buffer
	state := newExternalTurnState()

	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: "long", Detail: "big task"})
	for i := range maxTrackedExternalRuns + 5 {
		renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", TurnID: "turn:" + strconv.Itoa(i), Detail: "q"})
		// The long run keeps streaming the entire time, so it is never the
		// least-recently-used entry and must survive.
		renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "long", Content: "tick", Detail: "delta"})
	}

	starts := 0
	for _, l := range decodeNDJSONLines(t, buf.String()) {
		if l.Type == "external_turn_start" && l.RunID == "long" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("the still-streaming run produced %d external_turn_start lines, want 1; it was evicted while live", starts)
	}
	if !state.known("long") {
		t.Fatal("the still-streaming run was evicted; eviction is first-seen, not least-recently-used")
	}
}
