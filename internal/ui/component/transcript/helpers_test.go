package transcript

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// noticeEvent is a one-row block: header only, no body.
func noticeEvent(text string) uievent.Event {
	return uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: text}}
}

// drain applies every event in order and returns the final Model.
func drain(t *testing.T, m Model, evs []uievent.Event) Model {
	t.Helper()
	for _, ev := range evs {
		var cmd tea.Cmd
		m, cmd = m.HandleEvent(ev)
		_ = cmd
	}
	return m
}

// sized returns a measured model holding n one-row notice blocks.
func sizedModel(t *testing.T, width, height, n int) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(width, height)
	evs := make([]uievent.Event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, noticeEvent(blockName(i)))
	}
	return drain(t, m, evs)
}

// blockName is a token that is never a prefix of another token, so a
// containment check cannot pass for the wrong block.
func blockName(i int) string {
	return "n" + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + "|"
}
