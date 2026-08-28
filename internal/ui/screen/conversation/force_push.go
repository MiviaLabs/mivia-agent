package conversation

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
)

// forcePush parks text as the pending forced message and cancels the
// running turn. It is the single force mechanism: the turnEndedMsg
// drain is what actually sends. Parking rather than sending inline is
// required because Cancel does not synchronously release turnMu — an
// inline Send would deadlock against the turn still unwinding.
// If a force is already parked, that older text is demoted to the front
// of the queue and the user is notified; nothing is dropped.
// Reports false when the force could not be parked.
func (s *Screen) forcePush(text string) bool {
	if strings.TrimSpace(text) == "" || s.active == nil {
		return false
	}
	displaced := false
	var older string
	if s.pendingForce != nil {
		displaced = true
		older = *s.pendingForce
		s.queue = append([]string{older}, s.queue...)
		if s.queueOverlay.Active() {
			s.queueOverlay.SetItems(s.queue)
		}
	}
	s.pendingForce = &text
	s.approval.Clear()
	s.active.Cancel()
	s.statusline.Stop()
	if displaced {
		s.statusline.Notice("force-pushed; earlier force re-queued: " + preview(older))
	} else {
		s.statusline.Notice("force-pushed")
	}
	return true
}

// preview shortens text for a statusline notice.
func preview(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	const maxRunes = 40
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
}

// drainPendingForce is the shared drain helper the visible turnEndedMsg
// branch uses: it sends the parked forced text ahead of the ordinary
// queue via sendText. On send failure the text is demoted to the front
// of the ordinary queue instead of being dropped; pendingForce is nil
// either way once this returns handled=true.
func (s *Screen) drainPendingForce() (app.Screen, tea.Cmd, bool) {
	if s.pendingForce == nil {
		return *s, nil, false
	}
	forced := *s.pendingForce
	s.pendingForce = nil
	next, cmd := s.sendText(forced)
	sc := next.(Screen)
	if sc.active == nil {
		sc.queue = append([]string{forced}, sc.queue...)
		if sc.queueOverlay.Active() {
			sc.queueOverlay.SetItems(sc.queue)
		}
		sc.statusline.Notice("send failed; re-queued")
	}
	return sc, cmd, true
}
