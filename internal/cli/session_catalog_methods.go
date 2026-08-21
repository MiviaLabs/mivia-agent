package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// listSessions returns the user-facing session list: every real session plus
// one entry per conversation, with forked continuations (rows that display the
// same name from the same directory) collapsed to the newest row. Worktree
// routes and internal auto-save snapshots are never merged.
func (m *tuiModel) listSessions() ([]chat.SessionInfo, error) {
	infos, err := m.session.ListSessions()
	if err != nil {
		return nil, err
	}
	return collapseConversations(infos), nil
}

// deleteConversationGroup removes the visible row and every catalog row that
// belongs to the same conversation. The sidebar shows one row per
// conversation; deleting it must remove the hidden continuation rows too, or
// they resurface on the next refresh. The visible row is always deleted first
// (it may itself be an internal auto-save in legacy mode), then the hidden
// rows that display the same name from the same directory. Worktree routes are
// never grouped and are left untouched.
func (m *tuiModel) deleteConversationGroup(visible chat.SessionInfo) error {
	if err := m.session.DeleteSession(visible.Reference()); err != nil {
		return err
	}
	raw, err := m.session.ListSessions()
	if err != nil {
		return err
	}
	latestAuto := latestAutoSaveName(raw)
	for _, info := range raw {
		if info.WorktreeRoute || chat.IsAutoSaveName(info.Name) {
			continue
		}
		if displaySessionName(info, latestAuto) != displaySessionName(visible, latestAuto) || info.Dir != visible.Dir {
			continue
		}
		if err := m.session.DeleteSession(info.Reference()); err != nil {
			return err
		}
	}
	return nil
}
