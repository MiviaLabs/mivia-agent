package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type compactionEventMsg struct{ event ports.CompactionEvent }

func (s Screen) handleCompactionMessage(ev ports.CompactionEvent) (app.Screen, tea.Cmd) {
	if ev.SessionID != "" && ev.SessionID != s.convID() {
		return s, nil
	}
	return s.handleCompactionEvent(ev)
}

func (s Screen) handleCompactionKey(msg tea.KeyPressMsg) (app.Screen, tea.Cmd, bool) {
	if s.compaction == nil || msg.String() != "ctrl+c" {
		return s, nil, false
	}
	if s.compactionCancelRequested {
		next, cmd, _ := s.quit()
		return next, cmd, true
	}
	s.compaction.Cancel()
	s.compactionCancelRequested = true
	s.statusline.SetLabel("canceling")
	return s, nil, true
}

func nextCompactionEvent(h ports.CompactionHandle) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-h.Events()
		if !ok {
			return compactionEventMsg{event: ports.CompactionEvent{Done: true}}
		}
		return compactionEventMsg{event: ev}
	}
}

func (s Screen) startCompaction(h ports.CompactionHandle) (app.Screen, tea.Cmd) {
	s.compaction = h
	s.compactionSessionID = s.convID()
	s.compactionCancelRequested = false
	spinner := s.statusline.Start("compact", s.now())
	return s, tea.Batch(spinner, nextCompactionEvent(h))
}

func (s Screen) handleCompactionEvent(ev ports.CompactionEvent) (app.Screen, tea.Cmd) {
	if ev.Done {
		s.compaction = nil
		s.compactionSessionID = ""
		s.compactionCancelRequested = false
		s.statusline.Stop()
		if ev.Err != nil {
			s.statusline.Notice("compact failed: " + ev.Err.Error())
		} else if ev.Notice != "" {
			s.statusline.Notice(ev.Notice)
		}
		return s, nil
	}
	if ev.Phase != "" {
		s.statusline.SetLabel(ev.Phase)
	}
	s.statusline.SetDetail(ev.Detail)
	if s.compaction == nil {
		return s, nil
	}
	return s, nextCompactionEvent(s.compaction)
}
