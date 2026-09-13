package uiadapter_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/transcript"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func TestRenderSmoke_OneTurnHasOneModelFooter(t *testing.T) {
	m := transcript.New(loadTestTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m.SetModel("smoke-model")
	seq := []agent.Event{
		{Kind: agent.EventAssistant, Content: "hello"},
		{Kind: agent.EventTokenUsage, TokenUsage: &events.TokenUsageEvent{InputTokens: 10, OutputTokens: 20}},
	}
	for _, ev := range seq {
		for _, out := range uiadapter.TranslateEvent(ev) {
			m, _ = m.HandleEvent(out)
		}
	}
	plain := ansi.Strip(strings.Join(m.Rows(), "\n"))
	if got := strings.Count(plain, "smoke-model"); got != 1 {
		t.Fatalf("model footer appears %d times, want one:\n%s", got, plain)
	}
}
