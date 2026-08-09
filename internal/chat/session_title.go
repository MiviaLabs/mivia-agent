package chat

import (
	"context"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// titleFromFirstMessage converts a conversation opener into a display title:
// single line, whitespace collapsed, capped at 80 runes. It is display-only
// metadata and never enters the durable catalog.
func titleFromFirstMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	runes := []rune(msg)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// fillSessionTitles back-fills a display title for every untitled live context
// session in out. The title is the session's first user message, resolved
// through the store when it implements SessionFirstMessageSource. Named
// snapshots (no SessionID) and already-titled rows keep their existing name.
// Row identity and order are untouched, so internal consumers of ListSessions
// (auto-save detection, pruning, restore) see no change.
func fillSessionTitles(ctx context.Context, catalog contextstate.SessionCatalog, principal contextstate.Principal, out []SessionInfo) {
	src, ok := catalog.(contextstate.SessionFirstMessageSource)
	if !ok {
		return
	}
	for i := range out {
		if out[i].SessionID == "" || out[i].Title != "" {
			continue
		}
		opener, err := src.FirstUserMessage(ctx, principal, out[i].SessionID)
		if err != nil || opener == "" {
			continue
		}
		out[i].Title = titleFromFirstMessage(opener)
	}
}
