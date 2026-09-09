package conversation

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestNewSessionStateKeepsResumedReasoningSeparateFromAnswer(t *testing.T) {
	conv := newSessionStateHistoryConv()
	conv.history = []ports.Message{
		{Role: "user", Text: "question"},
		{Role: "assistant", Reasoning: "private plan", Text: "public answer"},
	}

	s := New(loadTheme(t), theme.TierASCII, nil, conv, nil, 80, func() time.Time {
		return time.Unix(1000, 0)
	})
	st := s.newSessionState(conv)

	var reasoning, answer bool
	for _, block := range st.transcript.Blocks() {
		switch block.Kind {
		case uievent.KindReasoning:
			reasoning = true
		case uievent.KindTextEnd:
			answer = block.Input == "public answer"
		}
	}
	if !reasoning || !answer {
		t.Fatalf("resumed blocks lost the reasoning/answer split: reasoning=%v answer=%v blocks=%+v", reasoning, answer, st.transcript.Blocks())
	}
}
