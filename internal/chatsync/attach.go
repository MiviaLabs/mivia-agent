package chatsync

import (
	"context"
	"errors"
	"fmt"
)

// SessionAttachment represents the established sync binding for a session.
type SessionAttachment struct {
	SessionID  string
	ServerSeq  int64
	FlushedSeq int64
	ForkedFrom string
}

// AttachSession connects to an existing remote session or creates a new remote session.
func AttachSession(ctx context.Context, client *Client, outbox *Outbox, params CreateSessionParams, existingSessionID string) (*SessionAttachment, error) {
	if existingSessionID != "" {
		sess, err := client.GetSession(ctx, existingSessionID)
		if err == nil {
			return &SessionAttachment{
				SessionID:  sess.ID,
				ServerSeq:  sess.LastSeq,
				FlushedSeq: outbox.Cursor().FlushedSeq,
			}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("get session %s: %w", existingSessionID, err)
		}
	}

	created, err := client.CreateSession(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &SessionAttachment{
		SessionID:  created.ID,
		ServerSeq:  created.LastSeq,
		FlushedSeq: 0,
	}, nil
}

// FlushOutbox sends all unflushed events from the outbox to the remote server.
// It advances the local outbox cursor only upon successful acknowledgment.
// If a 409 Conflict is returned, it returns ErrConflict so the session can fork.
func FlushOutbox(ctx context.Context, client *Client, outbox *Outbox, sessionID string) (int, error) {
	unflushed, err := outbox.UnflushedEvents()
	if err != nil {
		return 0, fmt.Errorf("read unflushed events: %w", err)
	}
	if len(unflushed) == 0 {
		return 0, nil
	}

	items := make([]EventItem, len(unflushed))
	for i, se := range unflushed {
		items[i] = EventItem{
			Seq:     se.Seq,
			Type:    se.Type,
			Payload: se.Payload,
		}
	}

	res, err := client.AppendEvents(ctx, sessionID, items)
	if err != nil {
		return 0, err
	}

	if err := outbox.AdvanceCursor(res.LastSeq); err != nil {
		return 0, fmt.Errorf("advance cursor: %w", err)
	}

	return res.InsertedCount, nil
}
