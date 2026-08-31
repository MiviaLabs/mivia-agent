package chatsync

import (
	"context"
	"encoding/json"
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
// When serverLastSeq > cursor.FlushedSeq:
// - If all events past cursor match writerID (or writerID is empty) -> ADOPT: ServerSeq = sess.LastSeq, FlushedSeq = sess.LastSeq
// - If any event has a different writerID -> FORK: end old session, create new session, ForkedFrom = oldID
func AttachSession(ctx context.Context, client *Client, outbox *Outbox, params CreateSessionParams, existingSessionID string, writerID string) (*SessionAttachment, error) {
	if existingSessionID != "" {
		sess, err := client.GetSession(ctx, existingSessionID)
		if err == nil {
			flushedSeq := outbox.Cursor().FlushedSeq
			if sess.LastSeq > flushedSeq && writerID != "" {
				count := int(sess.LastSeq - flushedSeq)
				events, readErr := client.GetEvents(ctx, existingSessionID, flushedSeq, count)
				if readErr == nil && len(events) > 0 {
					isForeign := false
					for _, ev := range events {
						var header struct {
							WriterID string `json:"writer_id"`
						}
						if err := json.Unmarshal(ev.Payload, &header); err == nil {
							if header.WriterID != "" && header.WriterID != writerID {
								isForeign = true
								break
							}
						}
					}
					if isForeign {
						_, _ = client.EndSession(ctx, existingSessionID)
						created, createErr := client.CreateSession(ctx, params)
						if createErr != nil {
							return nil, fmt.Errorf("fork session: %w", createErr)
						}
						return &SessionAttachment{
							SessionID:  created.ID,
							ServerSeq:  created.LastSeq,
							FlushedSeq: 0,
							ForkedFrom: existingSessionID,
						}, nil
					}
				}
				_ = outbox.AdvanceCursor(sess.LastSeq)
				return &SessionAttachment{
					SessionID:  sess.ID,
					ServerSeq:  sess.LastSeq,
					FlushedSeq: sess.LastSeq,
				}, nil
			}

			return &SessionAttachment{
				SessionID:  sess.ID,
				ServerSeq:  sess.LastSeq,
				FlushedSeq: flushedSeq,
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
