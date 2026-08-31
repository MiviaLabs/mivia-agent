//go:build livechat

package chatsync

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sseFrame is one server-sent event as it appears on the wire.
type sseFrame struct {
	Event string
	ID    string
	Data  string
}

// TestLiveChatSessionSSE checks the stream the tablet viewer will consume:
// historical replay from a cursor, then live push, over one connection.
//
// The wire framing is asserted rather than just the payload. A browser
// EventSource dispatches by the `event:` name, so what the server puts there
// decides whether a web client can subscribe at all.
func TestLiveChatSessionSSE(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "sse")

	// Seed history so the stream has something to replay. Without it the
	// response commits no headers until the first event, and the read below
	// would block on a connection that looks hung.
	a.appendEvents(ctx, s.ID, sampleEvents(1, 2), http.StatusOK)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := openSSE(t, streamCtx, a, s.ID, "?afterSeq=0")

	t.Run("replays history from the cursor", func(t *testing.T) {
		first := readFrame(t, frames, 20*time.Second)
		second := readFrame(t, frames, 20*time.Second)
		assertFrameSeq(t, first, 1)
		assertFrameSeq(t, second, 2)
	})

	t.Run("pushes live events on the open stream", func(t *testing.T) {
		// Append on a separate request while the stream is parked. This is the
		// desktop CLI writing and the tablet watching.
		a.appendEvents(ctx, s.ID, sampleEvents(3, 3), http.StatusOK)
		live := readFrame(t, frames, 20*time.Second)
		assertFrameSeq(t, live, 3)
	})

	t.Run("names the frame after the event type", func(t *testing.T) {
		a.appendEvents(ctx, s.ID, []eventItem{{
			Seq: 4, Type: "probe.custom", Payload: json.RawMessage(`{"n":4}`),
		}}, http.StatusOK)
		frame := readFrame(t, frames, 20*time.Second)

		if frame.Event == "" {
			t.Error("the frame carried no event name")
		}
		// A browser's EventSource.onmessage only fires for the default name.
		// If the server names frames after the client-supplied type, a web
		// viewer must addEventListener for every type it might ever see - and
		// type is an open string, so it cannot know them in advance.
		if frame.Event == "probe.custom" {
			t.Logf("FINDING: frames are named after the client-supplied event type (%q), not a fixed name. EventSource.onmessage will never fire; a web client must know every type up front.", frame.Event)
		}
		if frame.ID == "" {
			t.Error("the frame carried no id; without it a client cannot resume with Last-Event-ID")
		}
	})
}

// TestLiveChatSessionSSEResumesFromLastEventID checks the reconnect path a
// tablet needs after it sleeps: reopen with a cursor and receive only what was
// missed, with no duplicates.
func TestLiveChatSessionSSEResumesFromLastEventID(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "sse-resume")
	a.appendEvents(ctx, s.ID, sampleEvents(1, 5), http.StatusOK)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	frames := openSSE(t, streamCtx, a, s.ID, "?afterSeq=3")

	for want := int64(4); want <= 5; want++ {
		assertFrameSeq(t, readFrame(t, frames, 20*time.Second), want)
	}

	// Nothing below the cursor may be replayed: a viewer that resumes must not
	// re-render messages the user already read.
	select {
	case extra := <-frames:
		t.Errorf("stream sent an extra frame after the cursor range: event=%q id=%q data=%s",
			extra.Event, extra.ID, truncate([]byte(extra.Data)))
	case <-time.After(3 * time.Second):
	}
}

// openSSE opens the event stream and returns a channel of parsed frames. The
// connection closes when ctx is cancelled.
func openSSE(t *testing.T, ctx context.Context, a *api, sessionID, query string) <-chan sseFrame {
	t.Helper()
	url := a.baseURL + "/v1/chat-sessions/" + sessionID + "/events/stream" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.bearer)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := a.client.Do(req)
	if err != nil {
		t.Fatalf("open sse: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse content-type = %q, want text/event-stream", ct)
	}

	frames := make(chan sseFrame, 32)
	go scanSSE(resp.Body, frames)
	return frames
}

// scanSSE parses the line protocol into frames, emitting one per blank line.
func scanSSE(body interface{ Read([]byte) (int, error) }, frames chan<- sseFrame) {
	defer close(frames)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var current sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.Data != "" || current.Event != "" || current.ID != "" {
				frames <- current
				current = sseFrame{}
			}
		case strings.HasPrefix(line, ":"):
			// A keepalive comment. Ignored, but its presence is what stops an
			// idle intermediary from killing a quiet stream.
		case strings.HasPrefix(line, "event:"):
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "id:"):
			current.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if current.Data == "" {
				current.Data = chunk
			} else {
				current.Data += "\n" + chunk
			}
		}
	}
}

func readFrame(t *testing.T, frames <-chan sseFrame, wait time.Duration) sseFrame {
	t.Helper()
	select {
	case frame, ok := <-frames:
		if !ok {
			t.Fatal("the sse stream closed before the expected frame arrived")
		}
		return frame
	case <-time.After(wait):
		t.Fatalf("no sse frame within %v", wait)
		return sseFrame{}
	}
}

func assertFrameSeq(t *testing.T, frame sseFrame, want int64) {
	t.Helper()
	var decoded storedEvent
	if err := json.Unmarshal([]byte(frame.Data), &decoded); err != nil {
		t.Fatalf("sse data is not a session event: %v; data: %s", err, truncate([]byte(frame.Data)))
	}
	if decoded.Seq != want {
		t.Errorf("sse frame seq = %d, want %d", decoded.Seq, want)
	}
	if wantID := strconv.FormatInt(want, 10); frame.ID != "" && frame.ID != wantID {
		t.Errorf("sse frame id = %q, want %q; the id must be the seq for Last-Event-ID resume to work", frame.ID, wantID)
	}
}
