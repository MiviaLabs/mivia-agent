package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

var _ contextstate.SessionFirstMessageSource = (*SQLite)(nil)

// FirstUserMessage returns the first user message of a live context session,
// derived from the oldest complete checkpoint's active context. It is used to
// title untitled sessions in the picker. The lookup is subject-scoped: an
// older run's capability digest is stale by design, but its opener text is
// still readable by the subject that owns the catalog row.
//
// The whole oldest checkpoint is read: for a forked continuation the oldest
// checkpoint holds the full loaded history, which can exceed 64 KiB, and a
// byte-sliced prefix would break the JSON parse and void the title. A session
// with no complete checkpoint or no user message yields an empty string,
// never an error.
func (s *SQLite) FirstUserMessage(ctx context.Context, principal contextstate.Principal, sessionID string) (string, error) {
	if err := principal.Validate(); err != nil {
		return "", err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT active_context FROM context_checkpoints WHERE workspace_id=? AND subject_id=? AND session_id=? AND complete=1 ORDER BY source_start ASC, checkpoint_id LIMIT 1`, principal.WorkspaceID, principal.SubjectID, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var msgs []map[string]any
	if err := contextstate.UnmarshalCanonical(raw, &msgs); err != nil {
		return "", nil
	}
	for _, msg := range msgs {
		if role, _ := msg["role"].(string); role != "user" {
			continue
		}
		if isMemoryContextFrame(msg) {
			continue
		}
		if content, ok := msg["content"].(string); ok && content != "" {
			return content, nil
		}
	}
	return "", nil
}

// memoryContextMessageName mirrors chat.MemoryContextMessageName. It is
// duplicated as a literal, not imported: this package deliberately does not
// depend on internal/chat, and a cross-package contract test in internal/chat
// (system_prompt_compose_test.go) pins the two literals equal.
const memoryContextMessageName = "core-memory-context"

// memoryContextOpenTag is the frame's open tag. Legacy frames persisted
// before the sentinel Name existed carry no name, so the tag prefix is the
// secondary, display-only skip signal for them.
const memoryContextOpenTag = "<core-memory-context>"

// isMemoryContextFrame reports whether a persisted user message is the
// session-owned core-memory context frame. The frame always precedes the
// first real user objective, so without this skip every session title became
// the frame text. The check is display-only (titling): Name match first,
// open-tag prefix second for pre-Name legacy checkpoints.
func isMemoryContextFrame(msg map[string]any) bool {
	if name, _ := msg["name"].(string); name == memoryContextMessageName {
		return true
	}
	content, _ := msg["content"].(string)
	return strings.HasPrefix(content, memoryContextOpenTag)
}
