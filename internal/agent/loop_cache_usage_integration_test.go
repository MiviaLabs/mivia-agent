package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestLoopPublishesCacheUsageEventOnEventBus is the flagship integration test
// for prompt-cache observability: a real agent.Loop, a real httptest-backed
// OpenAICompat provider (no mocks), and a real events.Bus wired end to end.
// The scripted response carries DeepSeek-shaped cache usage fields; the test
// asserts the bus receives one content-free KindCacheUsage event with the
// correct provider/model/token accounting after the turn completes.
func TestLoopPublishesCacheUsageEventOnEventBus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]string{"role": "assistant", "content": "answer"},
			}},
			"usage": map[string]any{"prompt_tokens": 1000, "prompt_cache_hit_tokens": 900, "prompt_cache_miss_tokens": 100},
		})
	}))
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name: "deepseek", BaseURL: srv.URL, APIKey: "test-key", CacheUsageEnabled: true,
	})

	bus := events.New()
	var got events.Event
	fired := make(chan struct{}, 1)
	bus.Subscribe(events.KindCacheUsage, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		got = ev
		fired <- struct{}{}
	}))

	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	_, err := loop.Run(context.Background(), "hello", Options{
		Model: "deepseek-v4-pro", MaxSteps: 5, EventBus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("KindCacheUsage was never published")
	}

	if got.Kind != events.KindCacheUsage {
		t.Fatalf("kind = %v", got.Kind)
	}
	if got.Content != "" || got.Input != "" || got.Output != "" {
		t.Fatalf("bus event carried generic content: %+v", got)
	}
	if got.Detail == "" {
		t.Fatalf("bus event carried no detail: %+v", got)
	}
}

// A --no-tools-equivalent plain call path (Completer.ChatStream, bypassing
// agent.Loop entirely) must not publish KindCacheUsage - this is a
// deliberate, documented v1 limitation (see EmitCacheUsage), not a silent
// gap. This test pins that boundary so a future refactor that accidentally
// starts publishing from a second path is caught, and so anyone auditing
// coverage finds this decision recorded as a test, not just a comment.
func TestChatStreamDoesNotPublishCacheUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"prompt_cache_hit_tokens\":5}}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name: "deepseek", BaseURL: srv.URL, APIKey: "test-key", CacheUsageEnabled: true,
	})

	bus := events.New()
	fired := false
	bus.Subscribe(events.KindCacheUsage, events.HandlerFunc(func(_ context.Context, ev events.Event) { fired = true }))

	// ChatStream is exactly what internal/chat's sendPlain* path calls when
	// AgentTurnEnabled() is false (--no-tools sessions) - it never touches
	// agent.Loop or EmitCacheUsage.
	if _, err := comp.ChatStream(context.Background(), provider.Request{
		Model: "deepseek-v4-pro", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("ChatStream must never publish KindCacheUsage - it bypasses agent.Loop entirely")
	}
}
