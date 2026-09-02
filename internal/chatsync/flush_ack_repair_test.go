package chatsync

import (
	"encoding/json"
	"strings"
	"testing"
)

// The server repairs a payload it cannot otherwise store: a NUL is rejected by
// Postgres outright and takes the whole batch with it. Repair beats losing a
// hundred events - but it makes the stored body differ from the sent one, and
// this file's verification treats a difference as corruption.
//
// The two rules collide on the ambiguous-ack path. insertedCount comes back
// short because ON CONFLICT skipped rows already present, resolveAck reads the
// range back, the repaired body does not match, and the session's sync stops
// permanently. A batch-sized loss became a session-sized one.

func nulString() string { return "a" + string(rune(0)) + "b" }

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func sentEvent(t *testing.T, payload any) []StoredEvent {
	t.Helper()
	return []StoredEvent{{Seq: 1, Type: "mivia.chat.v1.tool.ended", Payload: raw(t, payload)}}
}

// TestARepairedBodyIsRecognisedAsOurOwn is the live defect: without it, one
// repaired event plus one ambiguous ack ends the session's sync for good.
func TestARepairedBodyIsRecognisedAsOurOwn(t *testing.T) {
	sent := sentEvent(t, map[string]any{"turn": "turn:1", "output": nulString()})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{
			"turn": "turn:1", "output": "ab", RepairedAtIngestKey: true,
		}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		t.Errorf("a body this client sent and the server repaired was read as "+
			"corruption, which stops the session permanently: %v", err)
	}
}

// TestARepairedBodyMayOnlySHRINK holds the tolerance to what a repair can
// actually do. "It was repaired, so anything goes" would reopen the corruption
// hole this verification exists to close.
func TestARepairedBodyMayOnlyShrink(t *testing.T) {
	sent := sentEvent(t, map[string]any{"turn": "turn:1", "output": "the output"})

	cases := []struct {
		name   string
		stored map[string]any
	}{
		{"a longer string", map[string]any{
			"turn": "turn:1", "output": "the output and more", RepairedAtIngestKey: true,
		}},
		{"a different string", map[string]any{
			"turn": "turn:1", "output": "something else", RepairedAtIngestKey: true,
		}},
		{"a changed sibling field", map[string]any{
			"turn": "turn:2", "output": "the output", RepairedAtIngestKey: true,
		}},
		{"an extra field", map[string]any{
			"turn": "turn:1", "output": "the output", "injected": "x", RepairedAtIngestKey: true,
		}},
		{"a missing field", map[string]any{
			"output": "the output", RepairedAtIngestKey: true,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := []StoredEvent{{
				Seq: 1, Type: "mivia.chat.v1.tool.ended", Payload: raw(t, tc.stored),
			}}
			if err := verifyStoredMatchesSent(sent, stored); err == nil {
				t.Errorf("%s passed as this client's own body; the repair flag is not "+
					"a licence for another writer to hold this seq", tc.name)
			}
		})
	}
}

// TestATruncatingRepairIsAccepted covers the other repair the server may make:
// cutting a payload that does not fit its column. A prefix of what was sent is
// still what was sent.
func TestATruncatingRepairIsAccepted(t *testing.T) {
	sent := sentEvent(t, map[string]any{"output": strings.Repeat("x", 100)})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{
			"output": strings.Repeat("x", 40), RepairedAtIngestKey: true,
		}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		t.Errorf("a truncating repair was read as corruption: %v", err)
	}
}

// TestAnUnflaggedDifferenceIsStillCorruption is the property that must NOT
// have been relaxed. A body that differs and carries no repair marker is
// another writer's, and the session must stop rather than interleave two
// transcripts.
func TestAnUnflaggedDifferenceIsStillCorruption(t *testing.T) {
	sent := sentEvent(t, map[string]any{"turn": "turn:1", "output": nulString()})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{"turn": "turn:1", "output": "ab"}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err == nil {
		t.Error("an unflagged body difference passed verification; a foreign writer " +
			"at this seq is exactly what this check exists to catch")
	}
}

// TestAnIdenticalBodyStillPasses guards the ordinary path, which is every
// healthy replay: same body, no marker, no repair logic involved.
func TestAnIdenticalBodyStillPasses(t *testing.T) {
	payload := map[string]any{"turn": "turn:1", "output": "the output", "n": float64(3)}
	sent := sentEvent(t, payload)
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended", Payload: raw(t, payload),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		t.Errorf("an identical body failed verification: %v", err)
	}
}

// TestASizeRepairIsRecognisedAsOurOwn is the defect a review measured: EVERY
// size repair the server performs was rejected here.
//
// A size repair shrinks a string AND writes a truncation record saying what it
// cut. That record is a key the sender never sent, so a strict comparison read
// the body as another writer's, raised ErrTranscriptConflict, and stopped the
// session's sync - reinstating the session-sized loss the repair exists to
// prevent, on the very retry path that motivates it.
func TestASizeRepairIsRecognisedAsOurOwn(t *testing.T) {
	sent := sentEvent(t, map[string]any{
		"turn":   "turn:1",
		"output": strings.Repeat("x", 100),
	})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{
			"turn":   "turn:1",
			"output": strings.Repeat("x", 40),
			// What a size repair adds, exactly as the API writes it.
			"trunc":             map[string]any{"fields": map[string]any{"output": map[string]any{"kept": 40, "total": 100}}},
			RepairedAtIngestKey: true,
		}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		t.Errorf("a size repair was read as corruption, which stops the session's "+
			"sync for good: %v", err)
	}
}

// TestARepairMayNotAddAnyOtherField holds the limit. Excluding the marker and
// the truncation record is what makes a repair recognisable; excluding
// anything else would let a foreign writer hide content in a new key.
func TestARepairMayNotAddAnyOtherField(t *testing.T) {
	sent := sentEvent(t, map[string]any{"turn": "turn:1", "output": "the output"})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{
			"turn": "turn:1", "output": "the output",
			"injected":          "something the sender never wrote",
			RepairedAtIngestKey: true,
		}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err == nil {
		t.Error("a stored body carrying an unknown extra key passed as this " +
			"client's own; only the marker and the truncation record are the " +
			"server's to add")
	}
}

// TestTheSendersOwnTruncationRecordDoesNotHaveToSurvive: the producer writes a
// trunc of its own when IT truncates, and a server repair may extend it. The
// record describes the body rather than being it, so it is excluded on both
// sides and a difference there is not corruption.
func TestTheSendersOwnTruncationRecordDoesNotHaveToSurvive(t *testing.T) {
	sent := sentEvent(t, map[string]any{
		"output": strings.Repeat("y", 80),
		"trunc":  map[string]any{"fields": map[string]any{"output": map[string]any{"kept": 80, "total": 900}}},
	})
	stored := []StoredEvent{{
		Seq: 1, Type: "mivia.chat.v1.tool.ended",
		Payload: raw(t, map[string]any{
			"output":            strings.Repeat("y", 30),
			"trunc":             map[string]any{"fields": map[string]any{"output": map[string]any{"kept": 30, "total": 900}}},
			RepairedAtIngestKey: true,
		}),
	}}

	if err := verifyStoredMatchesSent(sent, stored); err != nil {
		t.Errorf("a repair that extended the producer's own truncation record "+
			"was read as corruption: %v", err)
	}
}
