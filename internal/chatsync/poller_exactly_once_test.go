package chatsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestInputPoller_UndeliveredConsumedInputSurvivesShutdown pins the item-2
// defect: pollOnce used to call clearPendingInput() unconditionally after
// the delivery select, including on the ctx.Done()/stopCh branches. A
// server-marked-consumed input that was never actually placed on Inputs()
// (because the process is shutting down right as it arrives) had its durable
// local record deleted anyway - so restart recovery could never find it, and
// the instruction was lost even though the server believes it was handled.
//
// This test starves the poller's Inputs() channel (nobody ever reads it) and
// needs the delivery select to GENUINELY block on ctx.Done()/stopCh rather
// than merely race against Stop() - Inputs() is buffered (cap 16,
// internal/chatsync/poller.go), so a lone input's send never blocks at all,
// and an earlier version of this test that offered the same single input
// forever relied on the poll loop refilling that buffer fast enough to beat
// Stop() closing stopCh: under scheduling pressure, Go's select can instead
// pick the now-simultaneously-ready inputCh-send case over stopCh, flaking
// the test without any defect in the code under test. Serving 16 distinct
// filler inputs first fills the buffer deterministically, so the 17th
// (msgTargetID) can only ever be delivered by the stopCh branch - there is
// no room left for the send to be ready at all.
// msgShutdownTargetID names the 17th input newFillerThenTargetMockServer
// serves - the one that lands on a genuinely full buffer.
const msgShutdownTargetID = "inp-shutdown-target"

func TestInputPoller_UndeliveredConsumedInputSurvivesShutdown(t *testing.T) {
	stateDir := t.TempDir()
	srv := newFillerThenTargetMockServer(t)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-shutdown", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deliberately never read poller.Inputs(): the delivery attempt inside
	// pollOnce must block on the select until Stop() fires stopCh, exercising
	// the ctx.Done()/stopCh branch this test targets.
	poller.Start(ctx)

	pendingPath := filepath.Join(stateDir, pendingInputFileName)
	waitForTargetPendingAsConsumed(t, pendingPath)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	poller.Stop(stopCtx)

	data, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatalf("pending input file was cleared even though delivery never happened: %v", err)
	}
	var state pendingInputState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal pending state: %v", err)
	}
	if state.Input == nil || state.Input.ID != msgShutdownTargetID || !state.Consumed {
		t.Errorf("pending state = %+v, want the consumed %s record preserved", state, msgShutdownTargetID)
	}
}

// newFillerThenTargetMockServer offers 16 distinct filler inputs, then
// msgShutdownTargetID on every request after that - see
// TestInputPoller_UndeliveredConsumedInputSurvivesShutdown's doc comment for
// why 16 fillers are what makes the 17th's delivery deterministic.
func newFillerThenTargetMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	var served atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := served.Add(1)
		id := msgShutdownTargetID
		if n <= 16 {
			id = fmt.Sprintf("inp-filler-%d", n)
		}
		_ = json.NewEncoder(w).Encode(NextInput{
			Input: &SessionInput{
				ID:           id,
				SessionID:    "sess-shutdown",
				AuthorUserID: "user-1",
				Kind:         "message",
				Body:         "should survive an unread shutdown",
			},
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(SessionInput{
			ID:           r.PathValue("inputID"),
			SessionID:    "sess-shutdown",
			AuthorUserID: "user-1",
			Kind:         "message",
			Body:         "should survive an unread shutdown",
			ConsumedAt:   &now,
		})
	})
	return httptest.NewServer(mux)
}

// waitForTargetPendingAsConsumed waits for msgShutdownTargetID's OWN pending
// record - not just any file - so the caller only proceeds once the 16
// fillers have already been delivered and cleared and the 17th is the one
// genuinely stuck.
func waitForTargetPendingAsConsumed(t *testing.T, pendingPath string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if data, err := os.ReadFile(pendingPath); err == nil {
			var state pendingInputState
			if json.Unmarshal(data, &state) == nil && state.Input != nil && state.Input.ID == msgShutdownTargetID && state.Consumed {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the target input's pending file to appear as consumed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// fixedAuthorUserIDProvider returns an AuthorUserIDProvider that always
// resolves to id, for tests that are not about identity verification itself.
func fixedAuthorUserIDProvider(id string) AuthorUserIDProvider {
	return func(context.Context) (string, error) { return id, nil }
}
