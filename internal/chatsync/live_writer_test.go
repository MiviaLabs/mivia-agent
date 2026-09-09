//go:build livechat

package chatsync

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestLiveChatSessionPreservesWriterID pins the single assumption the entire
// adopt/fork mechanism rests on, and which nothing had ever observed.
//
// AttachSession decides "is this session mine?" ENTIRELY client-side: it reads
// the events the server holds past our cursor and compares the `writer_id`
// inside each PAYLOAD against ours (attach.go, the isForeign loop). The server
// has no writer concept at all - it never sees writer_id as a field, only as
// opaque bytes inside the payload it stores.
//
// That makes payload fidelity load-bearing in a way no other field is. If the
// API normalises, re-encodes, or drops unknown payload keys, writer_id comes
// back empty, `header.WriterID != ""` is false, isForeign stays false, and we
// ADOPT unconditionally. Two machines writing one session would then interleave
// into a single transcript with no fork and no warning - the exact permanent
// corruption REVIEW CHANGE 8 introduced writer ids to prevent, failing silently
// and in the safe-looking direction.
//
// The offline fake stores payloads verbatim because it is a Go map, so it can
// never catch this. Only a live probe can.
func TestLiveChatSessionPreservesWriterID(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "writer-id-fidelity")

	const writerID = "writer-fidelity-probe-0123456789"
	payload := json.RawMessage(`{"writer_id":"` + writerID + `","kind":"probe","nested":{"keep":"me"}}`)

	a.appendEvents(ctx, s.ID, []eventItem{{
		Seq: 1, Type: "mivia.chat.v1.turn.started", Payload: payload,
	}}, http.StatusOK)

	// A BARE ARRAY, not {"events":[...]}: the read-back endpoint's shape,
	// recorded here because an earlier draft of this probe guessed the
	// envelope and failed against the real response.
	var got []struct {
		Seq     int64           `json:"seq"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	status, raw := a.call(ctx, http.MethodGet, "/v1/chat-sessions/"+s.ID+"/events?afterSeq=0&limit=10", nil)
	if status != http.StatusOK {
		t.Fatalf("read-back returned %d, want 200. body: %s", status, truncate(raw))
	}
	if err := a.decodeInto(raw, &got); err != nil {
		t.Fatalf("decode read-back: %v. body: %s", err, truncate(raw))
	}
	if len(got) != 1 {
		t.Fatalf("read back %d events, want 1. body: %s", len(got), truncate(raw))
	}

	var header struct {
		WriterID string `json:"writer_id"`
		Nested   struct {
			Keep string `json:"keep"`
		} `json:"nested"`
	}
	if err := json.Unmarshal(got[0].Payload, &header); err != nil {
		t.Fatalf("the stored payload is not the object we sent: %v. payload: %s", err, truncate(got[0].Payload))
	}
	if header.WriterID != writerID {
		t.Fatalf("writer_id came back %q, want %q. The adopt/fork check reads this field out of the payload, so a server that does not return it verbatim makes every restart ADOPT: two machines merge into one transcript with no fork and no warning. payload: %s",
			header.WriterID, writerID, truncate(got[0].Payload))
	}
	if header.Nested.Keep != "me" {
		t.Errorf("a nested payload key was lost (nested.keep = %q, want \"me\"); the API is reshaping payloads, so other envelope fields are at risk too. payload: %s",
			header.Nested.Keep, truncate(got[0].Payload))
	}
}

// TestLiveChatSessionAcceptsAForeignWriter records what the API actually does
// when a SECOND writer appends to a session that already has one.
//
// The offline fake models this as a 409 (fakeAPI.ClaimForeignWriter), and an
// audit flagged that no live probe had ever exercised it. Reading attach.go
// shows why: there is no server-side writer contract to exercise. Detection is
// wholly client-side, so the server is expected to accept the append happily.
//
// This probe therefore asserts the SERVER's real behaviour rather than the
// fake's invention. If it ever starts returning 409 here, the client's fork
// logic is not the only thing that decides ownership any more, and attach must
// be revisited.
func TestLiveChatSessionAcceptsAForeignWriter(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "foreign-writer")

	a.appendEvents(ctx, s.ID, []eventItem{{
		Seq: 1, Type: "mivia.chat.v1.turn.started", Payload: json.RawMessage(`{"writer_id":"writer-A"}`),
	}}, http.StatusOK)

	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/events",
		map[string]any{"events": []map[string]any{{
			"seq": 2, "type": "mivia.chat.v1.turn.started",
			"payload": map[string]any{"writer_id": "writer-B"},
		}}})

	switch {
	case status == http.StatusConflict:
		t.Fatalf("the API rejected a second writer with 409. That is a SERVER-side ownership contract, which attach.go does not model - it decides ownership by reading writer_id back from payloads. attach must be revisited. body: %s", truncate(raw))
	case status >= 400:
		t.Fatalf("a second writer's append returned %d; the client's fork path assumes the append SUCCEEDS and that ownership is resolved on read-back. body: %s", status, truncate(raw))
	}

	// Both writers' events must be readable, since that is what attach reads
	// to detect the foreign one.
	_, readRaw := a.call(ctx, http.MethodGet, "/v1/chat-sessions/"+s.ID+"/events?afterSeq=0&limit=10", nil)
	if !strings.Contains(string(readRaw), "writer-A") || !strings.Contains(string(readRaw), "writer-B") {
		t.Errorf("read-back does not carry both writer ids; attach cannot detect a foreign writer it cannot see. body: %s", truncate(readRaw))
	}
}
