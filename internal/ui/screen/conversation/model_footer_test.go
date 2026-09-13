package conversation

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestSwitchConversationSetsModelOnFreshTranscript(t *testing.T) {
	convA := &backgroundTestConversation{id: "a", events: make(chan uievent.Event)}
	convB := &backgroundTestConversation{id: "b", events: make(chan uievent.Event)}
	s := newScreen(t, convA, nil, nil)
	s.sessions = nil
	s.switchConversation(convB)
	clock := time.Unix(1700000000, 0)
	s.transcript.Now = func() time.Time { return clock }
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}})
	s.transcript, _ = s.transcript.HandleEvent(uievent.Event{Kind: uievent.KindUsage, Body: uievent.UsageBody{InputTokens: 1}})
	if got := s.transcript.Blocks()[1].UsageModel; got != "model-b" {
		t.Fatalf("transcript model = %q, want model-b", got)
	}
}
