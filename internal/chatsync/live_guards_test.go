//go:build livechat

package chatsync

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestLiveChatSessionGuards checks the rejections a client depends on. Each
// one is a case where silently accepting would corrupt a transcript or leak
// another tenant's data, so a passing guard is worth more than a passing
// happy path.
func TestLiveChatSessionGuards(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "guards")

	t.Run("a forward sequence gap is rejected", func(t *testing.T) {
		// The CLI's whole restart story rests on this: if a gap were accepted,
		// a client that lost events would leave holes nobody could detect.
		_, raw := a.appendEvents(ctx, s.ID, []eventItem{{
			Seq: 50, Type: "probe.gap", Payload: json.RawMessage(`{}`),
		}}, http.StatusBadRequest)
		env := a.decodeError(raw)
		if !strings.Contains(strings.ToLower(string(env.Message)), "sequence") {
			t.Errorf("gap error message = %s; it should name the sequence problem", env.Message)
		}
	})

	t.Run("seq below 1 is rejected", func(t *testing.T) {
		a.appendEvents(ctx, s.ID, []eventItem{{
			Seq: 0, Type: "probe.zero", Payload: json.RawMessage(`{}`),
		}}, http.StatusBadRequest)
	})

	t.Run("an empty batch is rejected", func(t *testing.T) {
		status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/events",
			map[string]any{"events": []eventItem{}})
		if status != http.StatusBadRequest {
			t.Errorf("empty batch = %d, want 400; body: %s", status, truncate(raw))
		}
	})

	t.Run("a malformed uuid is rejected before the handler", func(t *testing.T) {
		status, raw := a.call(ctx, http.MethodGet, "/v1/chat-sessions/not-a-uuid", nil)
		if status != http.StatusBadRequest {
			t.Errorf("bad uuid = %d, want 400; body: %s", status, truncate(raw))
		}
	})

	t.Run("an unknown session is a 404, not a 403", func(t *testing.T) {
		// Uniform 404 is what stops this endpoint being an oracle for which
		// session ids exist in other organizations.
		status, raw := a.call(ctx, http.MethodGet,
			"/v1/chat-sessions/00000000-0000-4000-8000-000000000000", nil)
		if status != http.StatusNotFound {
			t.Errorf("unknown session = %d, want 404; body: %s", status, truncate(raw))
		}
	})

	t.Run("an unauthenticated request is refused", func(t *testing.T) {
		anon := &api{t: t, baseURL: a.baseURL, client: a.client}
		status, raw := anon.call(ctx, http.MethodGet, "/v1/chat-sessions", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("no bearer = %d, want 401; body: %s", status, truncate(raw))
		}
	})

	t.Run("waitSeconds above the ceiling is rejected", func(t *testing.T) {
		status, raw := a.call(ctx, http.MethodGet,
			"/v1/chat-sessions/"+s.ID+"/inputs/next?waitSeconds=600", nil)
		if status != http.StatusBadRequest {
			t.Errorf("waitSeconds=600 = %d, want 400; a client must not be able to park a connection indefinitely; body: %s", status, truncate(raw))
		}
	})

	t.Run("an over-long title is rejected", func(t *testing.T) {
		status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions",
			map[string]any{"title": strings.Repeat("x", 201)})
		if status != http.StatusBadRequest {
			t.Errorf("201-char title = %d, want 400; body: %s", status, truncate(raw))
		}
	})
}

// TestLiveChatSessionPayloadBoundIsRepairedNotRejected probes what a deployed
// API does with a payload over the column's 64 KiB bound.
//
// The contract CHANGED, and the reason is what rejecting cost. A rejection
// fails the whole request - a batch of up to a hundred events - and this
// client classifies a 400 that does not name a sequence gap as poison, which
// stops the session's sync permanently (session_badrequest.go). So one
// oversize event cost the entire remaining conversation, which is a far worse
// answer than losing the tail of one string.
//
// The API now shrinks an oversize payload to fit, records the cut in the
// payload's own trunc field, and marks it repaired_at_ingest so this client
// still recognises the body as its own (see RepairedAtIngestKey). A body far
// beyond any streaming agent's output is still refused outright.
//
// NOT RUN as part of this change: live probes need the operator to ask for
// them, so this assertion is written against the API in this repo pair and has
// not been executed against a deployment.
func TestLiveChatSessionPayloadBoundIsRepairedNotRejected(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "payload-bound")

	oversized := strings.Repeat("x", 70*1024)
	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/events",
		map[string]any{"events": []map[string]any{{
			"seq": 1, "type": "probe.oversized", "payload": map[string]any{"blob": oversized},
		}}})

	if status >= 500 {
		t.Fatalf("a 70 KiB payload returned %d; the server must never answer this with an error the client can only retry. body: %s", status, truncate(raw))
	}
	// ASSERT the status, do not merely log it. A 400 here is the OLD contract
	// and is now a failure: this client classifies a 400 with no sequence gap
	// in it as poison (session_badrequest.go), so a deployment that still
	// rejects would stop the session's sync on the first oversize event
	// instead of shrinking one string.
	if status != http.StatusOK {
		t.Fatalf("a 70 KiB payload returned %d, want 200 with the payload shrunk; a rejection here poisons the session and costs the rest of the conversation. body: %s", status, truncate(raw))
	}
}

// TestLiveChatSessionRejectsIntraBatchGap probes whether the sequence check
// looks past the first element of a batch.
//
// Only the batch's first seq is compared against the session high-water mark.
// If nothing checks the rest, a single request can write seq 1 and seq 99
// together, leaving a hole that lastSeq actively hides: lastSeq becomes 99, so
// a restarting CLI resumes at 100 and the missing 97 events are lost with no
// way to notice.
func TestLiveChatSessionRejectsIntraBatchGap(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "intra-batch-gap")

	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/events",
		map[string]any{"events": []eventItem{
			{Seq: 1, Type: "probe.a", Payload: json.RawMessage(`{}`)},
			{Seq: 99, Type: "probe.b", Payload: json.RawMessage(`{}`)},
		}})

	if status == http.StatusOK {
		var got appendResult
		_ = a.decodeInto(raw, &got)
		t.Fatalf("a batch containing an internal gap (seq 1 then 99) was accepted: lastSeq=%d insertedCount=%d. The session now reports a high-water mark of %d with 97 seqs that never existed, and a restarting CLI would resume past them.", got.LastSeq, got.InsertedCount, got.LastSeq)
	}
	// ASSERT, for the same reason as the payload bound above: the offline
	// fake commits to 400 here, and only this probe can say whether the
	// deployed API agrees.
	if status != http.StatusBadRequest {
		t.Fatalf("an intra-batch gap returned %d, want 400; the offline fake models 400, so any other status makes every gap test that relies on it a fiction. body: %s", status, truncate(raw))
	}
}

// TestLiveChatSessionConsumeIsExactlyOnce probes the consume handshake.
//
// The design promises exactly-once input delivery. A CLI implements that by
// acting on an input and then consuming it, so consume is the commit point. If
// a second consume of the same input also reports success, then two CLI
// processes -- or one CLI retrying after a timeout it never saw answered --
// both believe they own the input, and the user's instruction runs twice.
func TestLiveChatSessionConsumeIsExactlyOnce(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "consume-once")

	var queued sessionInput
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/inputs",
		map[string]any{"kind": "approval", "body": "yes"}, http.StatusCreated, &queued)

	var first sessionInput
	a.expect(ctx, http.MethodPost,
		"/v1/chat-sessions/"+s.ID+"/inputs/"+queued.ID+"/consume", nil, http.StatusOK, &first)
	if first.ConsumedAt == nil {
		t.Fatal("the first consume did not set consumedAt")
	}

	status, raw := a.call(ctx, http.MethodPost,
		"/v1/chat-sessions/"+s.ID+"/inputs/"+queued.ID+"/consume", nil)
	if status == http.StatusOK {
		t.Fatalf("consuming an already-consumed input returned 200. The loser of the race cannot tell it lost, so exactly-once cannot be built on this contract. body: %s", truncate(raw))
	}
	t.Logf("second consume rejected with %d", status)
}

// TestLiveChatSessionEndIsTerminal probes whether ending a session actually
// closes it for writes.
//
// End is refused for new inputs. If appends still succeed afterwards, "ended"
// means only "the tablet stops seeing new prompts", and a stale CLI can keep
// writing to a session the user believes is finished.
func TestLiveChatSessionEndIsTerminal(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "terminal")

	a.appendEvents(ctx, s.ID, sampleEvents(1, 1), http.StatusOK)
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/end", nil, http.StatusOK, nil)

	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/events",
		map[string]any{"events": sampleEvents(2, 2)})
	if status == http.StatusOK {
		t.Fatalf("events were appended to an ENDED session. Ending is not terminal for writes, so a session the user closed can still grow. body: %s", truncate(raw))
	}
	t.Logf("append to an ended session rejected with %d", status)
}
