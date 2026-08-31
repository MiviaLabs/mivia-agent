package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInputPollerReceivesAndConsumesInput(t *testing.T) {
	var pollCount int32
	var consumeCount int32
	var consumedID string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&pollCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			_ = json.NewEncoder(w).Encode(NextInput{
				Input: &SessionInput{
					ID:        "inp-99",
					SessionID: "sess-p-1",
					Kind:      "message",
					Body:      "remote user instruction",
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})

	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&consumeCount, 1)
		consumedID = r.PathValue("inputID")
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(SessionInput{
			ID:         consumedID,
			SessionID:  r.PathValue("id"),
			Kind:       "message",
			Body:       "remote user instruction",
			ConsumedAt: &now,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-p-1", 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller.Start(ctx)
	defer poller.Stop()

	select {
	case ri := <-poller.Inputs():
		if ri.ID != "inp-99" || ri.Body != "remote user instruction" {
			t.Errorf("received input = %+v", ri)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote input")
	}

	if atomic.LoadInt32(&consumeCount) != 1 || consumedID != "inp-99" {
		t.Errorf("consumeCount = %d, consumedID = %q; want 1, inp-99", atomic.LoadInt32(&consumeCount), consumedID)
	}
}

func TestInputPollerSuppressesDeliveryIfConsumeFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{
			Input: &SessionInput{
				ID:        "inp-bad",
				SessionID: "sess-p-2",
				Kind:      "message",
				Body:      "will fail consume",
			},
		})
	})

	mux.HandleFunc("POST /v1/chat-sessions/{id}/inputs/{inputID}/consume", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-p-2", 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poller.Start(ctx)
	defer poller.Stop()

	select {
	case ri := <-poller.Inputs():
		t.Fatalf("unexpected delivery of unconsumed input: %+v", ri)
	case <-time.After(100 * time.Millisecond):
		// Expected: no delivery
	}
}

func TestInputPoller_ChannelClosedOnStop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}/inputs/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(NextInput{Input: nil})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL})
	poller := NewInputPoller(client, "sess-p-3", 1)

	poller.Start(context.Background())
	time.Sleep(10 * time.Millisecond)
	poller.Stop()

	// Assert input channel is closed cleanly upon Stop
	select {
	case _, ok := <-poller.Inputs():
		if ok {
			t.Fatal("expected closed channel, got value")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for inputs channel to close")
	}
}
