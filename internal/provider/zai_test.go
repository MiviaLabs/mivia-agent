package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestZAIWireContractAndFlatErrors(t *testing.T) {
	const apiKey = "zai-secret-key"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/paas/v4/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("Accept-Language"); got != "en-US,en" {
			t.Errorf("accept-language=%q", got)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":4001,"message":"bad request `+apiKey+`"}`)
			return
		}
		if calls == 2 {
			_, _ = io.WriteString(w, `{"code":4002,"message":"successful error"}`)
			return
		}
		if calls == 3 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"complete","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"web_search":[{"title":"source"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	comp, err := New(&config.Resolved{ProviderName: "zai", BaseURL: srv.URL + "/api/paas/v4", APIKey: apiKey, APIKeySet: true})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "glm-5.2", Messages: []Message{{Role: RoleUser, Content: "private prompt"}}}
	_, err = comp.Chat(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "4001") || strings.Contains(err.Error(), apiKey) || len(err.Error()) > 400 {
		t.Fatalf("error=%q", err)
	}
	_, err = comp.Chat(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "4002") {
		t.Fatalf("error=%q", err)
	}
	response, err := comp.ChatTurn(context.Background(), req)
	if err != nil || response.Content != "complete" || response.FinishReason != "tool_calls" || len(response.WebSearch) != 1 || len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	text, err := comp.ChatStream(context.Background(), req, io.Discard)
	if err != nil || text != "streamed" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestZAIDefaultEndpointAndOpenAIEnvelopeFallback(t *testing.T) {
	comp, err := NewZAI(Options{APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if client := comp.(*OpenAICompat); client.baseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("baseURL=%q", client.baseURL)
	}
	if err := zaiErrorParser(http.StatusBadRequest, []byte(`{"error":{"message":"openai error","code":"x"}}`)); err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, body := range [][]byte{[]byte(`not json`), []byte(`{"code":12}`), []byte(`{"code":null,"message":"no code"}`)} {
		if err := zaiErrorParser(http.StatusBadRequest, body); err != nil {
			t.Fatalf("body=%q err=%v", body, err)
		}
	}
}
