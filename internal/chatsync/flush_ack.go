package chatsync

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// resolveAck decides how far the local cursor may move after an append.
//
// The old rule was AdvanceCursor(res.LastSeq), which is wrong twice over.
// res.LastSeq is the SESSION's high-water mark, not a statement about the batch
// that was just sent, so a server ahead of this client marked events flushed
// that it never received. And the server applies ON CONFLICT DO NOTHING, so
// insertedCount below the batch size means it kept bodies it already had -
// "`insertedCount: 0` reads as 'already applied, all good'", which is silent
// transcript corruption when those bodies are not this client's
// (chat-sync-cli-slice.md:150-152).
//
// The cursor may therefore only ever reach the last seq this client SENT, and
// only after a short ack has been shown to be this client's own replay.
func resolveAck(ctx context.Context, client *Client, sessionID string, sent []StoredEvent, res *AppendResult) (int64, error) {
	lastSent := sent[len(sent)-1].Seq

	if res.InsertedCount >= len(sent) {
		return lastSent, nil
	}

	stored, err := client.GetEvents(ctx, sessionID, sent[0].Seq-1, len(sent))
	if err != nil {
		// No evidence either way. A readback failure is a network condition,
		// not proof of corruption, so it stays retryable.
		return 0, fmt.Errorf("verify short append ack: %w", err)
	}
	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrTranscriptConflict, err)
	}
	return lastSent, nil
}

// verifyStoredMatchesSent confirms the server's copy of every sent seq is the
// body this client sent.
//
// Equality is semantic, not byte-for-byte: the payload round-trips through
// jsonb, which does not preserve key order or insignificant whitespace, so a
// byte comparison would report corruption on every healthy replay.
func verifyStoredMatchesSent(sent, stored []StoredEvent) error {
	byServerSeq := make(map[int64]StoredEvent, len(stored))
	for _, se := range stored {
		byServerSeq[se.Seq] = se
	}

	for _, want := range sent {
		got, ok := byServerSeq[want.Seq]
		if !ok {
			return fmt.Errorf("seq %d was neither inserted nor readable back", want.Seq)
		}
		if got.Type != want.Type {
			return fmt.Errorf("seq %d holds type %q, this client sent %q", want.Seq, got.Type, want.Type)
		}
		same, err := samePayload(want.Payload, got.Payload)
		if err != nil {
			return fmt.Errorf("seq %d: %w", want.Seq, err)
		}
		if !same {
			return fmt.Errorf("seq %d holds a body this client did not send", want.Seq)
		}
	}
	return nil
}

func samePayload(a, b json.RawMessage) (bool, error) {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false, fmt.Errorf("decode the local payload: %w", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false, fmt.Errorf("decode the stored payload: %w", err)
	}
	return reflect.DeepEqual(av, bv), nil
}
