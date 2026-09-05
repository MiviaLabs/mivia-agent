package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHeartbeatPeriodicAndImmediateTransition(t *testing.T) {
	var mu sync.Mutex
	var receivedStatuses []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		receivedStatuses = append(receivedStatuses, body["status"])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: body["status"]})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	runner := NewHeartbeatRunner(client, "sess-hb-1", 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.Start(ctx)

	// Verify initial heartbeat
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if len(receivedStatuses) == 0 || receivedStatuses[0] != "waiting_input" {
		t.Fatalf("receivedStatuses = %v, want initial 'waiting_input'", receivedStatuses)
	}
	mu.Unlock()

	// State transition triggers immediate heartbeat
	runner.SetStatus(ctx, "running")
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	foundRunning := false
	for _, s := range receivedStatuses {
		if s == "running" {
			foundRunning = true
			break
		}
	}
	mu.Unlock()

	if !foundRunning {
		t.Errorf("expected 'running' in received statuses %v", receivedStatuses)
	}

	runner.Stop(ctx)
}

func TestHeartbeatRunner_StopIdempotentAndRespectsContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	runner := NewHeartbeatRunner(client, "sess-hb-2", 1*time.Second)
	ctx := context.Background()

	runner.Start(ctx)
	// Stop should be idempotent (calling multiple times does not panic)
	runner.Stop(ctx)
	runner.Stop(ctx)

	// Stop with cancelled context returns immediately without blocking
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		runner.Stop(cancelCtx)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for runner.Stop to return on cancelled context")
	}
}
