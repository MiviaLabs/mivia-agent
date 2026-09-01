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

// maxAppendBatch caps how many events one AppendEvents request carries. The
// server enforces this itself - a probe against the real staging API
// returned a 400 "events must contain no more than 100 elements" for any
// larger batch - and critically, that specific 400 does NOT match
// handleBadRequest's IsSequenceComplaint check, so an oversized batch does
// not get the gap-rebase retry path: it falls straight to poison(), which
// stops the sync session PERMANENTLY for the rest of the process. A local
// outbox with more than 100 events unflushed (easy once
// [sync].stream_assistant multiplies event count, or on the final Stop
// flush after periodic mid-turn flushes fell behind) would poison itself on
// its very next flush attempt. FlushOutbox must never send more than this
// many events in one request.
const maxAppendBatch = 100

// FlushOutbox sends all unflushed events from the outbox to the remote
// server, in batches of at most maxAppendBatch events.
//
// The cursor advances only over events the server actually took for THAT
// batch, never to the session's high-water mark - see resolveAck. Each batch
// gets its own ack and cursor advance, so a failure partway through a large
// backlog leaves the cursor at the last successfully acked batch, not at 0:
// the next flush attempt (this call, retried, or the periodic ticker) resumes
// from there rather than resending what already landed. If a 409 Conflict is
// returned it returns ErrConflict so the session can stop.
func FlushOutbox(ctx context.Context, client *Client, outbox *Outbox, sessionID string) (int, error) {
	total := 0
	for {
		unflushed, err := outbox.UnflushedEvents()
		if err != nil {
			return total, fmt.Errorf("read unflushed events: %w", err)
		}
		if len(unflushed) == 0 {
			return total, nil
		}
		if ctx.Err() != nil {
			return total, ctx.Err()
		}

		n := len(unflushed)
		if n > maxAppendBatch {
			n = maxAppendBatch
		}
		chunk := unflushed[:n]

		items := make([]EventItem, len(chunk))
		for i, se := range chunk {
			items[i] = EventItem{
				Seq:     se.Seq,
				Type:    se.Type,
				Payload: se.Payload,
			}
		}

		res, err := client.AppendEvents(ctx, sessionID, items)
		if err != nil {
			return total, err
		}

		ackSeq, err := resolveAck(ctx, client, sessionID, chunk, res)
		if err != nil {
			return total, err
		}
		if err := outbox.AdvanceCursor(ackSeq); err != nil {
			return total, fmt.Errorf("advance cursor: %w", err)
		}
		total += res.InsertedCount

		if n < maxAppendBatch {
			return total, nil
		}
	}
}
