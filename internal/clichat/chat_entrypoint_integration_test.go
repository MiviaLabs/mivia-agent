package clichat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// fakeProviderServer answers OpenAI-compatible chat completions with one fixed
// assistant message and records the tool list and system prompt of every
// request, so an entrypoint test can assert what the model was actually
// advertised and what the prompt's deferred-tool index locked away.
type fakeProviderServer struct {
	mu        sync.Mutex
	toolNames [][]string
	prompts   []string
}

func (f *fakeProviderServer) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tools    []map[string]any `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	names := make([]string, 0, len(body.Tools))
	for _, spec := range body.Tools {
		fn, _ := spec["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	f.mu.Lock()
	f.toolNames = append(f.toolNames, names)
	for _, message := range body.Messages {
		if message.Role != "system" {
			continue
		}
		// The system message's content is cache-marked into an array of text
		// parts; stitch the parts back together so assertions can substring-
		// match the composed prompt. A plain string stays a plain string.
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(message.Content, &parts); err == nil && len(parts) > 0 {
			var b strings.Builder
			for _, part := range parts {
				b.WriteString(part.Text)
			}
			f.prompts = append(f.prompts, b.String())
			continue
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err == nil {
			f.prompts = append(f.prompts, content)
		}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
}

func (f *fakeProviderServer) advertised() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.toolNames))
	copy(out, f.toolNames)
	return out
}

func (f *fakeProviderServer) systemPrompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

// TestRunConfiguredChatOneShotAdvertisesTheWholeUnion drives the real chat
// entrypoint end to end - config, agent selection, workspace tools, context
// store, dispatcher attach, one-shot turn - against a stub provider, and
// asserts plan tools-advertising/01's wire contract: the request advertises
// the core tool, load_tools, AND every deferred candidate (grep, glob) -
// the whole admissible union is pinned on the wire from the first request,
// not withheld until load_tools admits it. Execution authority still starts
// narrow (that is a dispatcher-level concern, covered elsewhere); this test
// is about what the request itself serializes.
func TestRunConfiguredChatOneShotAdvertisesTheWholeUnion(t *testing.T) {
	fake := &fakeProviderServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(ws)
	writeTestAgent(t, config.UserAgentsDir(), "reader", `
name = "reader"
description = "reads"
tools = ["read_file", "grep", "glob"]
tools_core = ["read_file"]
`)

	res := &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      server.URL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.DefaultSubagentConfig,
		Tools:        config.ToolsConfig{},
	}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(t.TempDir(), "context.db")

	invocation := chatInvocation{prompt: "hello", workspacePath: ws, agent: "reader", plainUI: true}
	if err := runConfiguredChat(invocation, res); err != nil {
		t.Fatalf("runConfiguredChat: %v", err)
	}

	requests := fake.advertised()
	if len(requests) == 0 {
		t.Fatal("the stub provider was never called")
	}
	first := requests[0]
	if !slices.Contains(first, "read_file") {
		t.Fatalf("advertised = %v, want the core tool", first)
	}
	if !slices.Contains(first, "load_tools") {
		t.Fatalf("advertised = %v, want the discovery tool", first)
	}
	for _, deferred := range []string{"grep", "glob"} {
		if !slices.Contains(first, deferred) {
			t.Fatalf("advertised = %v, want %q advertised (locked, not withheld - plan tools-advertising/01)", first, deferred)
		}
	}
}

// TestRunConfiguredChatRefusesWithoutAnAPIKey pins the entrypoint's first gate.
func TestRunConfiguredChatRefusesWithoutAnAPIKey(t *testing.T) {
	// workspacePath isolates this test from the ambient main repository's
	// real .mivia/mivia.toml (see the note in
	// TestRunConfiguredChatCarriesResumeSessionAcrossRestart).
	err := runConfiguredChat(chatInvocation{workspacePath: t.TempDir()}, &config.Resolved{APIKeyEnv: "TEST_KEY"})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error = %v, want the missing-key refusal", err)
	}
}
