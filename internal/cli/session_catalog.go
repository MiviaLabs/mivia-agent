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

// conversationKey identifies one conversation in the user-facing list. A name
// shown in the picker plus the directory the session lived in is the best
// lineage signal the catalog exposes: each mivia run forks a new durable
// session row when a session is resumed, and the fork repeats the opener.
type conversationKey struct {
	name string
	dir  string
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

// collapseConversations keeps one row per conversation in a user-facing
// session list. Rows that display the same name from the same directory are
// continuations of one conversation, so only the newest stays visible (the
// list is newest-first). Worktree routes and internal auto-save snapshots are
// never merged: routes open a workspace, and auto-saves are durability
// artifacts, not conversation lineage.
func collapseConversations(infos []chat.SessionInfo) []chat.SessionInfo {
	latestAuto := latestAutoSaveName(infos)
	seen := make(map[conversationKey]bool, len(infos))
	out := make([]chat.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.WorktreeRoute || chat.IsAutoSaveName(info.Name) {
			out = append(out, info)
			continue
		}
		key := conversationKey{name: displaySessionName(info, latestAuto), dir: info.Dir}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, info)
	}
	return out
}
