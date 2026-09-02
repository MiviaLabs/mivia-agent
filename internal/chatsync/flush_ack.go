package chatsync

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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
		same, err := payloadMatches(want.Payload, got.Payload)
		if err != nil {
			return fmt.Errorf("seq %d: %w", want.Seq, err)
		}
		if !same {
			return fmt.Errorf("seq %d holds a body this client did not send", want.Seq)
		}
	}
	return nil
}

// RepairedAtIngestKey marks a payload the SERVER rewrote on the way in, so
// this client can recognise its own body in a form it did not send.
//
// The server repairs a payload it cannot otherwise store at all: a NUL is
// rejected by Postgres outright, and the rejection takes the whole batch with
// it. Repairing is better than losing a hundred events - but it makes stored
// and sent differ, and this file exists to treat that difference as
// corruption.
//
// Without the flag the two rules collide and the strict one wins: a repaired
// batch that is later re-sent (an ambiguous ack, where insertedCount comes
// back short because ON CONFLICT skipped rows already stored) reads back a
// body this client did not send, raises ErrTranscriptConflict, and stops the
// session's sync permanently. The repair turned a batch-sized loss into a
// session-sized one.
const RepairedAtIngestKey = "repaired_at_ingest"

// truncationKey is the record a size repair writes to say WHAT it cut. The
// wire already defines this field and a viewer already renders a badge for it,
// so a repair reports itself through it rather than inventing a vocabulary.
//
// It is listed here because a repair ADDS it: the stored body then has a key
// the sender never sent, and a strict key-count comparison reads that as
// another writer's body. Every size repair the server performs was rejected
// for exactly that reason, which stopped the session's sync - reinstating the
// loss the repair exists to prevent, on the retry path that motivated it.
const truncationKey = "trunc"

// payloadMatches reports whether the stored body is this client's own.
//
// Equality is semantic, not byte-for-byte: the payload round-trips through
// jsonb, which preserves neither key order nor insignificant whitespace.
// A body the server flagged as repaired is compared under the narrower rule
// in repairedValueMatches instead.
func payloadMatches(sent, stored json.RawMessage) (bool, error) {
	sentVal, storedVal, err := decodePair(sent, stored)
	if err != nil {
		return false, err
	}

	storedObj, ok := storedVal.(map[string]any)
	if !ok || storedObj[RepairedAtIngestKey] != true {
		return reflect.DeepEqual(sentVal, storedVal), nil
	}

	// Two fields are excluded from the comparison ON BOTH SIDES: the marker
	// that says a repair happened, and the truncation record that says what it
	// cut. Both describe the body rather than being it.
	//
	// Excluding them is what makes a size repair recognisable at all. A repair
	// ADDS the record, so the stored body has a key the sender never sent, and
	// a strict comparison read that as another writer's body - every size
	// repair the server performed was rejected for that reason, which stopped
	// the session's sync and reinstated the loss the repair exists to prevent.
	//
	// Excluding the record is safe because it is not content: a viewer renders
	// it as a badge of counts, never as transcript text. Everything a reader
	// actually sees is still compared, and still under the shrink-only rule.
	pruned := stripRepairMetadata(storedObj)
	sentPruned := sentVal
	if sentObj, ok := sentVal.(map[string]any); ok {
		sentPruned = stripRepairMetadata(sentObj)
	}
	return repairedValueMatches(sentPruned, pruned), nil
}

// repairedValueMatches is the tolerance, and it is deliberately narrow.
//
// A repair may only SHRINK a string: remove the code points the store cannot
// hold, and cut what does not fit. So a stored string must be the sent string
// with its NULs removed, or a prefix of that. Anything else - a different key,
// a changed number, a longer string - is another writer's body, which is
// exactly what this verification exists to catch. Accepting "it was repaired,
// so anything goes" would reopen the corruption hole rather than close it.
func repairedValueMatches(sent, stored any) bool {
	switch want := sent.(type) {
	case string:
		got, ok := stored.(string)
		if !ok {
			return false
		}
		shrunk := strings.ReplaceAll(want, "\x00", "")
		return got == shrunk || strings.HasPrefix(shrunk, got)
	case map[string]any:
		got, ok := stored.(map[string]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for k, v := range want {
			other, present := got[k]
			if !present || !repairedValueMatches(v, other) {
				return false
			}
		}
		return true
	case []any:
		got, ok := stored.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if !repairedValueMatches(want[i], got[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(sent, stored)
	}
}

func decodePair(a, b json.RawMessage) (any, any, error) {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return nil, nil, fmt.Errorf("decode the local payload: %w", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return nil, nil, fmt.Errorf("decode the stored payload: %w", err)
	}
	return av, bv, nil
}

// stripRepairMetadata removes the two fields that describe a repair rather
// than forming part of the body.
func stripRepairMetadata(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		if k == RepairedAtIngestKey || k == truncationKey {
			continue
		}
		out[k] = v
	}
	return out
}
