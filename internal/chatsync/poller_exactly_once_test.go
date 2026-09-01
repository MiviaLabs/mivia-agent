package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
// This test starves the poller's Inputs() channel (nobody ever reads it),
// lets a real consumed input arrive, then calls Stop() while it is still
// sitting unconsumed-by-us in the poller's own buffered channel. The pending
// state file must still be on disk afterward: an input that was never
// confirmed delivered must not have its record cleared.
func TestInputPoller_UndeliveredConsumedInputSurvivesShutdown(t *testing.T) {
	stateDir := t.TempDir()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{
			Input: &SessionInput{
				ID:           "inp-shutdown",
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
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-shutdown", 1, fixedAuthorUserIDProvider("user-1"), stateDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deliberately never read poller.Inputs(): the delivery attempt inside
	// pollOnce must block on the select until Stop() fires stopCh, exercising
	// the ctx.Done()/stopCh branch this test targets.
	poller.Start(ctx)

	// Give pollOnce time to reach (and block inside) the delivery select.
	deadline := time.After(2 * time.Second)
	pendingPath := filepath.Join(stateDir, pendingInputFileName)
	for {
		if _, err := os.Stat(pendingPath); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the pending input file to appear")
		case <-time.After(10 * time.Millisecond):
		}
	}

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
	if state.Input == nil || state.Input.ID != "inp-shutdown" || !state.Consumed {
		t.Errorf("pending state = %+v, want the consumed inp-shutdown record preserved", state)
	}
}

// fixedAuthorUserIDProvider returns an AuthorUserIDProvider that always
// resolves to id, for tests that are not about identity verification itself.
func fixedAuthorUserIDProvider(id string) AuthorUserIDProvider {
	return func(context.Context) (string, error) { return id, nil }
}
