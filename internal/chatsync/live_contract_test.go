//go:build livechat

package chatsync

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestLiveChatSessionLifecycle walks one session through the sequence a CLI
// will actually perform: register, push events, read them back by cursor,
// heartbeat, receive a remote input, consume it, and end.
//
// The subtests share a session and run in order, because seq is monotonic per
// session and an input consumed in one step must be gone in the next.
func TestLiveChatSessionLifecycle(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "lifecycle")

	t.Run("register returns a running session at seq 0", func(t *testing.T) {
		checkRegistration(t, s)
	})
	t.Run("append advances the high-water mark", func(t *testing.T) {
		checkAppend(t, ctx, a, s.ID)
	})
	t.Run("replaying a batch is idempotent", func(t *testing.T) {
		checkReplayIdempotent(t, ctx, a, s.ID)
	})
	t.Run("cursor read returns events after seq, in order", func(t *testing.T) {
		checkCursorRead(t, ctx, a, s.ID)
	})
	t.Run("limit bounds the page", func(t *testing.T) {
		checkLimit(t, ctx, a, s.ID)
	})
	t.Run("heartbeat updates status", func(t *testing.T) {
		checkHeartbeat(t, ctx, a, s.ID)
	})
	t.Run("long poll times out with a null input", func(t *testing.T) {
		checkPollTimeout(t, ctx, a, s.ID)
	})

	var queued sessionInput
	t.Run("a parked poll wakes when an input arrives", func(t *testing.T) {
		queued = checkPollWakeup(t, ctx, a, s.ID)
	})
	t.Run("consume marks the input consumed", func(t *testing.T) {
		checkConsume(t, ctx, a, s.ID, queued)
	})
	t.Run("a consumed input is no longer delivered", func(t *testing.T) {
		checkNoRedelivery(t, ctx, a, s.ID)
	})
	t.Run("end marks the session ended", func(t *testing.T) {
		checkEnd(t, ctx, a, s.ID)
	})
	t.Run("input to an ended session is refused", func(t *testing.T) {
		checkInputAfterEnd(t, ctx, a, s.ID)
	})
}

func checkRegistration(t *testing.T, s session) {
	t.Helper()
	if s.ID == "" {
		t.Fatal("no session id")
	}
	if s.Status != "running" {
		t.Errorf("status = %q, want running", s.Status)
	}
	if s.LastSeq != 0 {
		t.Errorf("lastSeq = %d, want 0; a CLI starts its first batch at seq 1", s.LastSeq)
	}
	if s.EndedAt != nil {
		t.Errorf("endedAt = %v, want null", *s.EndedAt)
	}
	if s.OrganizationID == "" || s.UserID == "" {
		t.Error("the session is not attributed to an org and user")
	}
	assertRFC3339(t, "createdAt", s.CreatedAt)
	assertRFC3339(t, "lastEventAt", s.LastEventAt)
}

func checkAppend(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	got, _ := a.appendEvents(ctx, id, sampleEvents(1, 3), http.StatusOK)
	if got.LastSeq != 3 {
		t.Errorf("lastSeq = %d, want 3", got.LastSeq)
	}
	if got.InsertedCount != 3 {
		t.Errorf("insertedCount = %d, want 3", got.InsertedCount)
	}
}

// checkReplayIdempotent covers the property that lets a CLI retry an append it
// never saw acknowledged. Without it, a flaky network duplicates transcript.
func checkReplayIdempotent(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	got, _ := a.appendEvents(ctx, id, sampleEvents(1, 3), http.StatusOK)
	if got.InsertedCount != 0 {
		t.Errorf("insertedCount = %d on replay, want 0: the batch was duplicated", got.InsertedCount)
	}
	if got.LastSeq != 3 {
		t.Errorf("lastSeq = %d after replay, want 3", got.LastSeq)
	}
}

func checkCursorRead(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	var events []storedEvent
	a.expect(ctx, http.MethodGet, "/v1/chat-sessions/"+id+"/events?afterSeq=1", nil, http.StatusOK, &events)
	if len(events) != 2 {
		t.Fatalf("got %d events after seq 1, want 2", len(events))
	}
	if events[0].Seq != 2 || events[1].Seq != 3 {
		t.Errorf("seqs = %d,%d, want 2,3", events[0].Seq, events[1].Seq)
	}
	if events[0].Payload["n"] == nil {
		t.Error("payload did not round-trip")
	}
	assertRFC3339(t, "event createdAt", events[0].CreatedAt)
}

func checkLimit(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	var events []storedEvent
	a.expect(ctx, http.MethodGet, "/v1/chat-sessions/"+id+"/events?afterSeq=0&limit=1", nil, http.StatusOK, &events)
	if len(events) != 1 {
		t.Fatalf("got %d events with limit=1, want 1", len(events))
	}
	if events[0].Seq != 1 {
		t.Errorf("first page seq = %d, want 1", events[0].Seq)
	}
}

func checkHeartbeat(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	var got session
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions/"+id+"/heartbeat",
		map[string]any{"status": "waiting_input"}, http.StatusOK, &got)
	if got.Status != "waiting_input" {
		t.Errorf("status = %q, want waiting_input", got.Status)
	}
}

func checkPollTimeout(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	start := time.Now()
	var got nextInput
	a.expect(ctx, http.MethodGet, "/v1/chat-sessions/"+id+"/inputs/next?waitSeconds=2",
		nil, http.StatusOK, &got)
	if got.Input != nil {
		t.Fatalf("got an input on an empty queue: %+v", got.Input)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("returned after %v; waitSeconds=2 should park, not return immediately", elapsed)
	}
}

func checkConsume(t *testing.T, ctx context.Context, a *api, id string, queued sessionInput) {
	t.Helper()
	var got sessionInput
	a.expect(ctx, http.MethodPost,
		"/v1/chat-sessions/"+id+"/inputs/"+queued.ID+"/consume", nil, http.StatusOK, &got)
	if got.ConsumedAt == nil {
		t.Fatal("consumedAt is null after consume")
	}
	if got.ID != queued.ID {
		t.Errorf("consumed %q, want %q", got.ID, queued.ID)
	}
}

func checkNoRedelivery(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	var got nextInput
	a.expect(ctx, http.MethodGet, "/v1/chat-sessions/"+id+"/inputs/next?waitSeconds=0",
		nil, http.StatusOK, &got)
	if got.Input != nil {
		t.Errorf("a consumed input was delivered again: %+v", got.Input)
	}
}

func checkEnd(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	var got session
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions/"+id+"/end", nil, http.StatusOK, &got)
	if got.Status != "ended" {
		t.Errorf("status = %q, want ended", got.Status)
	}
	if got.EndedAt == nil {
		t.Error("endedAt is null on an ended session")
	}
}

func checkInputAfterEnd(t *testing.T, ctx context.Context, a *api, id string) {
	t.Helper()
	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+id+"/inputs",
		map[string]any{"kind": "message", "body": "after the end"})
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", status, truncate(raw))
	}
}

// checkPollWakeup parks a long poll, posts an input while it is parked, and
// measures how long the poll takes to return.
//
// This is the interaction the whole feature exists for: someone taps a reply
// on a tablet and the CLI in another room acts on it. If the rendezvous misses,
// the poll still returns the input on its NEXT cycle, so a naive test would
// pass while the user waits 25 seconds. Timing the wakeup is the only way to
// tell a working rendezvous from a slow fallback.
func checkPollWakeup(t *testing.T, ctx context.Context, a *api, sessionID string) sessionInput {
	t.Helper()

	done := make(chan pollResult, 1)
	go func() {
		start := time.Now()
		var got nextInput
		a.expect(ctx, http.MethodGet,
			"/v1/chat-sessions/"+sessionID+"/inputs/next?waitSeconds=25", nil, http.StatusOK, &got)
		done <- pollResult{got: got, elapsed: time.Since(start)}
	}()

	// Give the poll time to reach the server and park before the input lands.
	time.Sleep(1500 * time.Millisecond)

	var queued sessionInput
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions/"+sessionID+"/inputs",
		map[string]any{"kind": "message", "body": "steer from the tablet"},
		http.StatusCreated, &queued)
	if queued.ConsumedAt != nil {
		t.Error("a newly queued input is already consumed")
	}
	return awaitWakeup(t, done, queued)
}

// pollResult is what the parked long poll returned and how long it waited.
type pollResult struct {
	got     nextInput
	elapsed time.Duration
}

func awaitWakeup(t *testing.T, done <-chan pollResult, queued sessionInput) sessionInput {
	t.Helper()
	select {
	case result := <-done:
		if result.got.Input == nil {
			t.Fatal("the parked poll returned null while an input was queued")
		}
		if result.got.Input.ID != queued.ID {
			t.Errorf("poll returned input %q, want %q", result.got.Input.ID, queued.ID)
		}
		// The design doc claims sub-200ms. Allow generous slack for a real
		// network and a cold container, but stay far below the 25s ceiling:
		// anything near that means the waiter was never woken and the input
		// was picked up by a fallback path.
		if result.elapsed > 10*time.Second {
			t.Errorf("the parked poll took %v to see the input; the wakeup did not happen and it fell through to the timeout path", result.elapsed)
		}
		t.Logf("poll wakeup latency: %v", result.elapsed)
		return queued
	case <-time.After(40 * time.Second):
		t.Fatal("the long poll never returned")
		return sessionInput{}
	}
}

// sampleEvents builds a contiguous batch from lo to hi inclusive.
func sampleEvents(lo, hi int64) []eventItem {
	events := make([]eventItem, 0, hi-lo+1)
	for seq := lo; seq <= hi; seq++ {
		events = append(events, eventItem{
			Seq:     seq,
			Type:    "probe.message",
			Payload: map[string]any{"n": seq, "text": "live probe event"},
		})
	}
	return events
}

func assertRFC3339(t *testing.T, field, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("%s is empty", field)
		return
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Errorf("%s = %q is not RFC3339: %v; a Go client cannot decode it into time.Time", field, value, err)
	}
}
