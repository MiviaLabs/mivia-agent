package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNoReplayRequestDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/redirected" {
			redirected.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"},"finish_reason":"stop"}]}`))
			return
		}
		http.Redirect(w, req, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: server.URL, APIKey: "k"})
	_, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, DisableProviderReplay: true,
	})
	if err == nil {
		t.Fatal("redirect response succeeded")
	}
	if got := redirected.Load(); got != 0 {
		t.Fatalf("redirected requests = %d, want 0", got)
	}
}
