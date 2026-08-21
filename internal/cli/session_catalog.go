package cli

import (
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// LatestAutoSaveName returns the most recently updated auto-save name in infos
// (ListSessions order is newest-first; first auto match wins). Relocated from
// internal/legacytui/welcome.go: needed unqualified there (the welcome
// picker) and here (conversation collapsing).
func LatestAutoSaveName(infos []chat.SessionInfo) string {
	for _, si := range infos {
		if chat.IsAutoSaveName(si.Name) {
			return si.Name
		}
	}
	return ""
}

// DisplaySessionName labels sessions for the welcome picker.
// Latest auto-save → "Last session"; older autos → "Auto · {relative time}";
// named sessions keep their name. Handles bare __last__ and __last__* names.
func DisplaySessionName(si chat.SessionInfo, latestAuto string) string {
	if si.WorktreeRoute {
		return "Worktree · " + si.Worktree
	}
	if !chat.IsAutoSaveName(si.Name) {
		if si.Title != "" {
			return si.Title
		}
		return si.Name
	}
	if latestAuto != "" && si.Name == latestAuto {
		return "Last session"
	}
	// Single auto without explicit latest, or matching legacy bare name as sole latest.
	if latestAuto == "" {
		return "Last session"
	}
	age := FormatSessionAge(si.UpdatedAt)
	if age != "" {
		return "Auto · " + age
	}
	return "Auto"
}

// FormatSessionAge returns a short relative time. Relocated from
// internal/legacytui/welcome.go alongside DisplaySessionName, its only
// caller in both packages.
func FormatSessionAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// conversationKey identifies one conversation in the user-facing list. A name
// shown in the picker plus the directory the session lived in is the best
// lineage signal the catalog exposes: each mivia run forks a new durable
// session row when a session is resumed, and the fork repeats the opener.
type conversationKey struct {
	name string
	dir  string
}

// CollapseConversations keeps one row per conversation in a user-facing
// session list. Rows that display the same name from the same directory are
// continuations of one conversation, so only the newest stays visible (the
// list is newest-first). Worktree routes and internal auto-save snapshots are
// never merged: routes open a workspace, and auto-saves are durability
// artifacts, not conversation lineage.
func CollapseConversations(infos []chat.SessionInfo) []chat.SessionInfo {
	latestAuto := LatestAutoSaveName(infos)
	seen := make(map[conversationKey]bool, len(infos))
	out := make([]chat.SessionInfo, 0, len(infos))
	for _, info := range infos {
		if info.WorktreeRoute || chat.IsAutoSaveName(info.Name) {
			out = append(out, info)
			continue
		}
		key := conversationKey{name: DisplaySessionName(info, latestAuto), dir: info.Dir}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, info)
	}
	return out
}
