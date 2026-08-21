package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// conversationKey identifies one conversation in the user-facing list. A name
// shown in the picker plus the directory the session lived in is the best
// lineage signal the catalog exposes: each mivia run forks a new durable
// session row when a session is resumed, and the fork repeats the opener.
type conversationKey struct {
	name string
	dir  string
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
