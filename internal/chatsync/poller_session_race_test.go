package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestInputPoller_ConsumeInputUsesTheSameSessionIDAsNextInput pins two
// things: no data race on p.sessionID (run this test with -race), and the
// stronger correctness property the race hid - ConsumeInput must be called
// with the EXACT session id NextInput just fetched raw.ID from, even if
// SetSessionID races in between. A poller that read p.sessionID fresh at
// the ConsumeInput call site could send a consume for inputID against
// whatever session SetSessionID just switched to, not the one the input
// actually came from.
//
// This is the dynamic half of DC-29 (locked capture, unlocked reuse -
// .agents/quality/defect-taxonomy.md): the static half,
// mivia.go.no-locked-field-reread (semgrep/agent-standards.yml), catches a
// capture and its reread sitting in the same block, but cannot see a reread
// reached through a different function or a setter with a new caller. This
// test is what closes that gap for p.sessionID specifically - see DC-29's
// probes section before assuming the semgrep rule alone is coverage.
func TestInputPoller_ConsumeInputUsesTheSameSessionIDAsNextInput(t *testing.T) {
	consumeSessionID, reachedNextHandler, srv := newSessionRaceMockServer(t)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-original", 1, fixedAuthorUserIDProvider("user-1"), t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Race SetSessionID against the poll loop's own read - this is exactly
	// what the fix's -race coverage depends on to catch a regression. Held
	// off until the first /inputs/next request actually lands - see
	// reachedNextHandler's doc comment below.
	go func() {
		<-reachedNextHandler
		for i := 0; i < 50; i++ {
			poller.SetSessionID("sess-switched")
		}
	}()

	poller.Start(ctx)
	defer poller.Stop(context.Background())

	// Drain Inputs() concurrently (whether validateRemoteInput accepts or
	// rejects the delivery depends on whether SetSessionID's race won before
	// or after validation runs, which is not what this test pins - either
	// outcome is fine here, this test cares only about what ConsumeInput was
	// called with).
	go func() {
		for range poller.Inputs() {
		}
	}()

	deadline := time.After(2 * time.Second)
	for consumeSessionID.Load() == nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ConsumeInput to be called")
		case <-time.After(5 * time.Millisecond):
		}
	}

	got, _ := consumeSessionID.Load().(string)
	if got != "sess-original" {
		t.Errorf("ConsumeInput was called with session id %q, want the id NextInput actually used (sess-original)", got)
	}
}

// newSessionRaceMockServer builds the fake API for
// TestInputPoller_ConsumeInputUsesTheSameSessionIDAsNextInput: it offers one
// SessionInput under "sess-original" and records which session id every
// ConsumeInput request actually named.
//
// reachedNextHandler closes once the server has actually received the FIRST
// /inputs/next request. Go evaluates NextInput's sessID argument (and the
// request is built with it) before that request ever leaves the client, so
// this signal guarantees pollOnce's local sessID was still "sess-original" -
// a racer must not call SetSessionID before this closes, or it could
// legitimately win BEFORE that first capture and make "sess-switched"
// self-consistently correct for both calls, which would not exercise the
// window this test targets (the gap between NextInput returning and
// ConsumeInput being called).
func newSessionRaceMockServer(t *testing.T) (consumeSessionID *atomic.Value, reachedNextHandler <-chan struct{}, srv *httptest.Server) {
	t.Helper()
	consumeSessionID = &atomic.Value{}
	var nextCount int32
	ch := make(chan struct{})
	var closeOnce sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		closeOnce.Do(func() { close(ch) })
		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&nextCount, 1) == 1 {
			// SetSessionID races with the rest of this handler returning;
			// pollOnce must still consume against the id THIS response was
			// served for (sess-original), not whatever SetSessionID lands on.
			_ = json.NewEncoder(w).Encode(NextInput{
				Input: &SessionInput{ID: "inp-race", SessionID: "sess-original", AuthorUserID: "user-1", Kind: "message", Body: "hi"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		consumeSessionID.Store(r.PathValue("id"))
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(SessionInput{
			ID: r.PathValue("inputID"), SessionID: "sess-original", AuthorUserID: "user-1", Kind: "message", Body: "hi", ConsumedAt: &now,
		})
	})
	return consumeSessionID, ch, httptest.NewServer(mux)
}
