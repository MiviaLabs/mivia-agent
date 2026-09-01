package conversation

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// oversizedTurnFixtureEventCount deliberately exceeds turnBufferSize (32,
// internal/uiadapter/conversation.go): 1 turn.start + 19 tool.start + 19
// tool.end + 1 turn.end = 40. This is the exact regression shape that made
// the deleted forwardRemoteInputs code (0a709d80) invisible under normal
// testing - a turn under 32 events never filled the buffer, so nothing
// exposed that the old code left the channel undrained.
const oversizedTurnFixtureEventCount = 40

// buildOversizedFixtureTurn returns a scripted turn with MORE events than
// the real per-turn channel buffers. turnStartInput becomes the leading
// turn.start event's Input, mirroring how the real
// internal/uiadapter.Conversation.Send derives its synthetic turn.start
// from PersistedText (falling back to Text) - see
// oversizedTurnConversation.Send below, which computes the same value.
func buildOversizedFixtureTurn(turnStartInput string) []uievent.Event {
	events := make([]uievent.Event, 0, oversizedTurnFixtureEventCount)
	events = append(events, uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: turnStartInput},
	})
	for i := 0; i < 19; i++ {
		id := fmt.Sprintf("call-%d", i)
		events = append(events, uievent.Event{
			Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: "noop"},
		})
		events = append(events, uievent.Event{
			Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: id, Name: "noop", OK: true},
		})
	}
	events = append(events, uievent.Event{
		Kind: uievent.KindTurnEnd,
		Body: uievent.TurnEndBody{Reason: "completed"},
	})
	return events
}

// oversizedTurnConversation is a ports.Conversation whose Send spawns a REAL
// producer goroutine blocking on a cap-32 channel, exactly like
// internal/uiadapter's Conversation.Send does in production. A consumer that
// does not keep draining stalls this goroutine forever on the 33rd event -
// the failure this test exists to catch.
type oversizedTurnConversation struct {
	id   string
	sent []string
}

func (c *oversizedTurnConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sent = append(c.sent, in.Text)
	// Same derivation internal/uiadapter/conversation.go's real Send uses:
	// PersistedText, when set, is what the transcript's turn.start shows.
	displayText := in.Text
	if in.PersistedText != "" {
		displayText = in.PersistedText
	}
	ch := make(chan uievent.Event, 32)
	go func() {
		defer close(ch)
		for _, ev := range buildOversizedFixtureTurn(displayText) {
			ch <- ev
		}
	}()
	return &testTurnHandle{id: "turn-" + c.id, events: ch}, nil
}

func (c *oversizedTurnConversation) History() []ports.Message             { return nil }
func (c *oversizedTurnConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *oversizedTurnConversation) Model() ports.ModelInfo {
	return ports.ModelInfo{Name: "fixture"}
}
func (c *oversizedTurnConversation) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *oversizedTurnConversation) Title() string             { return "Fixture" }
func (c *oversizedTurnConversation) ID() string                { return c.id }

// cmdPump fans an arbitrary number of concurrently-armed Cmds into one
// channel, mirroring how the real bubbletea runtime drives Update: it never
// waits for one Cmd to finish before running another. This matters here
// specifically because awaitRemoteInput's re-armed read Cmd blocks forever
// once the single scripted input has been consumed (there is no second one
// coming) - a driver that waited on Cmds one at a time, in order, would
// treat that legitimate steady-state block as a deadlock.
type cmdPump struct {
	msgs chan tea.Msg
}

func newCmdPump() *cmdPump { return &cmdPump{msgs: make(chan tea.Msg, 256)} }

func (p *cmdPump) spawn(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() { p.msgs <- cmd() }()
}

// TestSmoke_RemoteInput_OversizedTurnDrainsFully is the REQUIRED offline
// smoke test for the remote-input turn-ownership fix: it scripts a remote
// input through SetRemoteInputs -> handleRemoteInput -> conv.Send ->
// awaitSessionEvent, exactly the path a real tablet-originated instruction
// takes, and asserts per-kind event counts on a fixture turn that emits MORE
// than the 32-event per-turn buffer. The old, deleted shape
// (forwardRemoteInputs, internal/uiadapter/session_pool.go pre-0a709d80)
// called conv.Send and threw the handle away: nothing ever drained its
// events, so the agent loop's producer goroutine blocked forever on event
// 33 and Conversation.Send's turnMu then blocked the NEXT local send too.
// This test proves the replacement - the SCREEN draining its own
// awaitSessionEvent Cmd, the same path a local send uses - does not
// reproduce that: all 40 events arrive.
func TestSmoke_RemoteInput_OversizedTurnDrainsFully(t *testing.T) {
	dark, _, themes := themePair(t)
	conv := &oversizedTurnConversation{id: "sess-remote"}
	remoteCh := make(chan ports.RemoteInputEvent, 1)

	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	s.SetRemoteInputs(remoteCh)
	// Tall enough that nothing scrolls out of the viewport: the assertion
	// below reads the rendered transcript for the (via web) tag, and a
	// realistic 24-row terminal would scroll it off after 19 tool blocks.
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 2000})
	scr := next.(Screen)

	initCmd := scr.Init()
	if initCmd == nil {
		t.Fatal("Init() returned a nil Cmd; the remote-input read loop was never armed")
	}

	remoteCh <- ports.RemoteInputEvent{
		ID: "input-1", SessionID: "sess-remote", Body: "run the fixture turn", ReceivedAt: time.Now(),
	}

	scr, counts, total := drainRemoteInputTurn(t, scr, initCmd)

	if total != oversizedTurnFixtureEventCount {
		t.Errorf("total events drained = %d, want %d - the buffer-33 regression drops everything past the fill point",
			total, oversizedTurnFixtureEventCount)
	}
	if counts[uievent.KindTurnStart] != 1 {
		t.Errorf("KindTurnStart count = %d, want 1", counts[uievent.KindTurnStart])
	}
	if counts[uievent.KindToolStart] != 19 {
		t.Errorf("KindToolStart count = %d, want 19", counts[uievent.KindToolStart])
	}
	if counts[uievent.KindToolEnd] != 19 {
		t.Errorf("KindToolEnd count = %d, want 19", counts[uievent.KindToolEnd])
	}
	if counts[uievent.KindTurnEnd] != 1 {
		t.Errorf("KindTurnEnd count = %d, want 1", counts[uievent.KindTurnEnd])
	}

	if len(conv.sent) != 1 {
		t.Fatalf("conv.Send call count = %d, want 1", len(conv.sent))
	}
	if conv.sent[0] != "run the fixture turn" {
		t.Errorf("conv.Send Text = %q, want the raw body (Text must never carry the via-web tag - it reaches the model)", conv.sent[0])
	}

	view := scr.View()
	if !strings.Contains(view, "(via web)") {
		t.Errorf("rendered transcript is missing the (via web) tag: %s", view)
	}
	if strings.Count(view, "(via web)") != 1 {
		t.Errorf("(via web) tag appears %d times in the rendered transcript, want exactly 1", strings.Count(view, "(via web)"))
	}

	if scr.active != nil {
		t.Error("turn.end was drained but Screen.active is still set")
	}
}

// drainRemoteInputTurn drives scr's Cmd/Msg loop starting from initCmd,
// concurrently (see cmdPump), until a turnEndedMsg is observed. It returns
// the final Screen, the per-Kind count of every uievent.EventMsg processed,
// and their total.
func drainRemoteInputTurn(t *testing.T, scr Screen, initCmd tea.Cmd) (Screen, map[uievent.Kind]int, int) {
	t.Helper()
	pump := newCmdPump()
	pump.spawn(initCmd)

	counts := map[uievent.Kind]int{}
	total := 0
	turnEnded := false
	deadline := time.After(5 * time.Second)
	iterations := 0

	for !turnEnded {
		iterations++
		if iterations > 2000 {
			t.Fatalf("exceeded iteration bound (%d events counted so far, turnEnded=%v); the drain loop likely never terminates", total, turnEnded)
		}
		var msg tea.Msg
		select {
		case msg = <-pump.msgs:
		case <-deadline:
			buf := make([]byte, 1<<16)
			n := runtime.Stack(buf, true)
			t.Fatalf("timed out waiting for the turn to finish draining (%d/%d events counted)\n%s",
				total, oversizedTurnFixtureEventCount, buf[:n])
		}
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				pump.spawn(c)
			}
			continue
		}
		nextScreen, nextCmd := scr.Update(msg)
		scrType, ok := nextScreen.(Screen)
		if !ok {
			t.Fatalf("Update returned a non-Screen app.Screen for msg %T", msg)
		}
		scr = scrType
		switch m := msg.(type) {
		case uievent.EventMsg:
			counts[m.Event.Kind]++
			total++
		case turnEndedMsg:
			turnEnded = true
		}
		pump.spawn(nextCmd)
	}
	return scr, counts, total
}
