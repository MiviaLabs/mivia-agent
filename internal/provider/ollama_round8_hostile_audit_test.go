package provider

// Round-8 hostile functional audit (0dccb870..HEAD): the four gate sites must
// agree on ONE rule - keyless construction is allowed iff the provider is
// ollama AND config.IsOllamaLoopback(base_url) - and the keyless client must
// behave end to end against a real loopback daemon (no auth header, tools,
// streaming, retry). TEST-ONLY: additive, touches no production code.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// gateCase is one (provider, base_url, key) combination for the NewForProvider
// gate matrix. wantKeyless asserts whether construction must succeed without a
// key (keyless loopback ollama) and must otherwise fail with the missing-key
// error.
type gateCase struct {
	name       string
	provider   string
	baseURL    string
	apiKeySet  bool
	apiKey     string
	wantOK     bool // construction succeeds without a key
	wantKeyErr bool // construction fails specifically with the missing-key error
}

func runGateMatrix(t *testing.T, cases []gateCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &config.Resolved{
				ProviderName: tc.provider,
				ProviderRuntimes: map[string]config.ProviderRuntime{
					tc.provider: {ProviderName: tc.provider, BaseURL: tc.baseURL, APIKeyEnv: "TEST_KEY", APIKeySet: tc.apiKeySet, APIKey: tc.apiKey},
				},
			}
			comp, err := NewForProvider(res, tc.provider)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("NewForProvider(%s, %q) = %v, want keyless success", tc.baseURL, tc.provider, err)
				}
				if comp == nil {
					t.Fatal("nil completer on success")
				}
				return
			}
			if err == nil {
				comp.(*OpenAICompat).client.CloseIdleConnections()
				t.Fatalf("NewForProvider(%s, %q) succeeded with no key, want failure", tc.baseURL, tc.provider)
			}
			if tc.wantKeyErr && !strings.Contains(err.Error(), "missing API key") {
				t.Fatalf("NewForProvider error = %q, want missing-key error", err)
			}
			if !tc.wantKeyErr && strings.Contains(err.Error(), "missing API key") {
				t.Fatalf("NewForProvider error = %q, must not be a missing-key error", err)
			}
		})
	}
}

// TestRound8GateMatrixNewForProvider exhaustively walks (provider, base_url,
// key) and asserts the single documented rule: keyless succeeds ONLY for
// (ollama, loopback-literal), every other provider/base_url/keyless combo
// fails with the missing-key error, and no non-ollama provider ever reaches
// keyless mode at a loopback base_url.
func TestRound8GateMatrixNewForProvider(t *testing.T) {
	// localhost variants need a sane resolver; 127.0.0.1/::1 do not.
	installLocalhostResolverIPs(t, []string{"127.0.0.1"})
	cases := []gateCase{
		// The only legal keyless combos.
		{"ollama ipv4 keyless", "ollama", "http://127.0.0.1:11434/v1", false, "", true, false},
		{"ollama ipv6 keyless", "ollama", "http://[::1]:11434/v1", false, "", true, false},
		{"ollama localhost keyless", "ollama", "http://localhost:11434/v1", false, "", true, false},
		{"ollama LOCALHOST keyless", "ollama", "http://LOCALHOST:11434/v1", false, "", true, false},
		// Keyed construction always succeeds.
		{"ollama cloud keyed", "ollama", "https://ollama.com/v1", true, "k", true, false},
		{"ollama loopback keyed", "ollama", "http://127.0.0.1:11434/v1", true, "k", true, false},
		// Non-ollama providers: never keyless, even at a loopback base_url.
		{"deepseek loopback keyless", "deepseek", "http://127.0.0.1:11434/v1", false, "", false, true},
		{"openrouter loopback keyless", "openrouter", "http://127.0.0.1:11434/v1", false, "", false, true},
		{"zai loopback keyless", "zai", "http://127.0.0.1:11434/v1", false, "", false, true},
		// Ollama at a NON-loopback base_url must still demand a key.
		{"ollama cloud keyless", "ollama", "https://ollama.com/v1", false, "", false, true},
		{"ollama http remote keyless", "ollama", "http://ollama.example.com/v1", false, "", false, true},
		{"ollama localhost-dot keyless", "ollama", "http://localhost.:11434/v1", false, "", false, true},
		{"ollama suffix-host keyless", "ollama", "http://127.0.0.1.evil.com/v1", false, "", false, true},
		{"ollama userinfo keyless", "ollama", "http://u@127.0.0.1:11434/v1", false, "", false, true},
		// Whitespace key is treated as no key.
		{"ollama cloud blank key", "ollama", "https://ollama.com/v1", true, "   ", false, true},
		{"ollama loopback blank key", "ollama", "http://127.0.0.1:11434/v1", true, "   ", true, false},
	}
	runGateMatrix(t, cases)
}

// TestRound8GateMatrixNew drives the same rule through the New(res) entry
// point, where the active provider name comes from res.ProviderName.
func TestRound8GateMatrixNew(t *testing.T) {
	installLocalhostResolverIPs(t, []string{"127.0.0.1"})
	// Keyless ollama loopback via New(res): must construct.
	res := &config.Resolved{
		ProviderName: "ollama",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {ProviderName: "ollama", BaseURL: "http://127.0.0.1:11434/v1", APIKeyEnv: "TEST_KEY", APIKeySet: false},
		},
	}
	comp, err := New(res)
	if err != nil {
		t.Fatalf("New(keyless loopback ollama) = %v, want success", err)
	}
	client := comp.(*OpenAICompat)
	if client.apiKey != "" {
		t.Fatalf("keyless client kept apiKey %q", client.apiKey)
	}
	client.client.CloseIdleConnections()

	// Keyless ollama CLOUD via New(res): must fail with missing key.
	resCloud := &config.Resolved{
		ProviderName: "ollama",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeyEnv: "TEST_KEY", APIKeySet: false},
		},
	}
	if _, err := New(resCloud); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("New(cloud ollama, no key) = %v, want missing-key error", err)
	}

	// Non-ollama at a loopback base_url via New(res): must fail.
	resDS := &config.Resolved{
		ProviderName: "deepseek",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"deepseek": {ProviderName: "deepseek", BaseURL: "http://127.0.0.1:11434/v1", APIKeyEnv: "TEST_KEY", APIKeySet: false},
		},
	}
	if _, err := New(resDS); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("New(deepseek at loopback, no key) = %v, want missing-key error", err)
	}
}

// TestRound8KeylessEndToEndChat runs a keyless ollama client against a real
// loopback daemon (httptest binds 127.0.0.1): no Authorization header ever,
// tools round-trip, and the request path is /v1/chat/completions.
func TestRound8KeylessEndToEndChat(t *testing.T) {
	var sawAuth atomic.Bool
	var sawPath atomic.Bool
	var sawTools atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		if strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			sawPath.Store(true)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tools"`) {
			sawTools.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []any{map[string]any{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "read_file",
							"arguments": `{"path":"go.mod"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
		})
	}))
	defer daemon.Close()

	baseURL := daemon.URL + "/v1"
	comp, err := NewOllama(Options{BaseURL: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	client.client.Timeout = 30 * time.Second

	resp, err := client.ChatTurn(context.Background(), Request{
		Model:  "gpt-oss:120b",
		Stream: false,
		Messages: []Message{
			{Role: RoleUser, Content: "read go.mod"},
		},
		Tools: []ToolSpec{
			{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool_calls = %+v, want one read_file call", resp.ToolCalls)
	}
	if !sawPath.Load() {
		t.Fatal("request did not reach /v1/chat/completions")
	}
	if !sawTools.Load() {
		t.Fatal("request body did not carry tools")
	}
	if sawAuth.Load() {
		t.Fatal("keyless ollama request carried an Authorization header")
	}
}

// TestRound8KeylessEndToEndToolResult runs the tool-call replay turn
// (assistant tool-call + tool result) against a keyless loopback daemon and
// asserts the daemon receives the tool message and answers with plain content.
func TestRound8KeylessEndToEndToolResult(t *testing.T) {
	var sawAuth atomic.Bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"role":"tool"`) || !strings.Contains(string(body), `"tool_call_id":"call_1"`) {
			t.Errorf("tool-result turn missing tool message: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "done reading"},
			}},
		})
	}))
	defer daemon.Close()

	comp, err := NewOllama(Options{BaseURL: daemon.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	client.client.Timeout = 30 * time.Second

	var toolCall ToolCall
	toolCall.ID = "call_1"
	toolCall.Type = "function"
	toolCall.Function.Name = "read_file"
	toolCall.Function.Arguments = `{"path":"go.mod"}`

	out, err := client.Chat(context.Background(), Request{
		Model: "gpt-oss:120b",
		Messages: []Message{
			{Role: RoleUser, Content: "read go.mod"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{toolCall}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "package provider"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "done reading" {
		t.Fatalf("tool-result turn out = %q, want %q", out, "done reading")
	}
	if sawAuth.Load() {
		t.Fatal("keyless tool-result turn carried an Authorization header")
	}
}

// TestRound8KeylessEndToEndStream runs the streaming path against the loopback
// daemon: SSE deltas are forwarded live, assembled in order, and the stream
// ends cleanly on [DONE].
func TestRound8KeylessEndToEndStream(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo "}}]}`,
		`{"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}]}`,
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("stream request carried an Authorization header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer daemon.Close()

	comp, err := NewOllama(Options{BaseURL: daemon.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	client.client.Timeout = 30 * time.Second

	var live strings.Builder
	out, err := client.ChatStream(context.Background(), Request{
		Model:        "gpt-oss:120b",
		Messages:     []Message{{Role: RoleUser, Content: "hi"}},
		StreamWriter: &live,
	}, &live)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Hello world" {
		t.Fatalf("streamed content = %q, want %q", out, "Hello world")
	}
	if live.String() != "Hello world" {
		t.Fatalf("live writer received %q, want %q", live.String(), "Hello world")
	}
}

// TestRound8EdgeURLConstruction drives adversarial base_urls through the full
// keyless construction path. The invariant: construction either succeeds into
// a pinned loopback client (loopback spelling, including malformed-but-
// loopback forms the hostname-literal predicate approves) or fails closed -
// never a keyless client with an unpinned dial.
func TestRound8EdgeURLConstruction(t *testing.T) {
	installLocalhostResolverIPs(t, []string{"127.0.0.1"})
	cases := []struct {
		baseURL    string
		wantOK     bool
		wantPinned bool
	}{
		{"http://127.0.0.1:11434/v1/", true, true}, // trailing slash
		{"http://127.0.0.1:11434/v1///", true, true},
		{"http://127.0.0.1:0/v1", true, true},     // port 0 (dial refused, but construction ok)
		{"http://LOCALHOST:11434/v1", true, true}, // case-insensitive
		{"http://[::1]:11434/v1", true, true},
		{"http://localhost:11434/v1", true, true},
		// Malformed-but-loopback forms: the locked plan §3.1 predicate is
		// hostname-literal, so these APPROVE and construct a keyless client
		// whose dial is pinned to verified loopback. TestRound8PinnedDial-
		// CoversApprovedEdgeURLs proves ANY dial target (even 203.0.113.7) is
		// rewritten to loopback, so keyless traffic can never leave the
		// machine. The malformed part fails at request/dial time with a clear
		// error (empty port -> scheme-default port 80 refusal; trailing space
		// -> daemon 404).
		{"http://127.0.0.1:/v1", true, true},       // empty port: gate approves (hostname literal), dial pinned
		{"http://127.0.0.1:11434/v1 ", true, true}, // trailing space: path-only, dial pinned
		// Fails closed (no key, not a loopback spelling).
		{"http://localhost.:11434/v1", false, false}, // trailing dot
		{"http://127.0.0.1.evil.com/v1", false, false},
		{"http://u@127.0.0.1:11434/v1", false, false},
		{"https://ollama.com/v1", false, false}, // cloud without key: gate denies
	}
	for _, tc := range cases {
		t.Run(tc.baseURL, func(t *testing.T) {
			comp, err := NewForProvider(&config.Resolved{
				ProviderName: "ollama",
				ProviderRuntimes: map[string]config.ProviderRuntime{
					"ollama": {ProviderName: "ollama", BaseURL: tc.baseURL, APIKeyEnv: "TEST_KEY", APIKeySet: false},
				},
			}, "ollama")
			if tc.wantOK {
				if err != nil {
					t.Fatalf("NewForProvider(%q) = %v, want success", tc.baseURL, err)
				}
				client := comp.(*OpenAICompat)
				tr, ok := innerTransport(client).(*http.Transport)
				if !ok || tr.DialContext == nil {
					t.Fatalf("base_url %q produced an UNPINNED keyless client (DialContext nil)", tc.baseURL)
				}
				client.client.CloseIdleConnections()
				return
			}
			if err == nil {
				comp.(*OpenAICompat).client.CloseIdleConnections()
				t.Fatalf("NewForProvider(%q) succeeded without a key, want fail-closed", tc.baseURL)
			}
			if !tc.wantPinned && !strings.Contains(err.Error(), "missing API key") {
				// Any non-loopback spelling must fail with the missing-key gate,
				// not with an unexpected error (both are fail-closed, but the
				// documented error is the key gate).
				t.Logf("base_url %q failed closed with: %v", tc.baseURL, err)
			}
		})
	}
}

// TestRound8BlankKeyNeverUnpins checks that a whitespace-only key at a CLOUD
// ollama base_url cannot slip through the gate as "keyed": the NewForProvider
// gate uses TrimSpace, so construction must fail with missing-key.
func TestRound8BlankKeyNeverUnpins(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "ollama",
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {ProviderName: "ollama", BaseURL: "https://ollama.com/v1", APIKeyEnv: "TEST_KEY", APIKeySet: true, APIKey: " \t "},
		},
	}
	if _, err := NewForProvider(res, "ollama"); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("whitespace key at cloud base_url = %v, want missing-key error", err)
	}
}

// TestRound8PinnedDialCoversApprovedEdgeURLs proves the security invariant
// that actually matters: EVERY URL the hostname-literal predicate approves
// still produces a pinned dial to a loopback address, so the looser-than-doc
// predicate cannot cause keyless traffic to leave the machine.
func TestRound8PinnedDialCoversApprovedEdgeURLs(t *testing.T) {
	// POSITIVE, routing-independent proof (round-9 hardening): the pinned dial
	// must rewrite ANY target - here a TEST-NET-3 address the host would never
	// reach - to a verified loopback address and CONNECT. An unpinned dial to
	// the same address fails (timeout / no route), so a pass is unambiguous.
	ln4, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln4.Close()
	acceptAndClose(ln4)
	v4port := ln4.Addr().(*net.TCPAddr).Port

	urls := []struct {
		raw    string
		target string // TEST-NET-3 address whose port matches a live listener
	}{
		{"http://127.0.0.1:11434/v1 ", net.JoinHostPort("203.0.113.7", strconv.Itoa(v4port))},
		{"http://127.0.0.1:11434/v1#", net.JoinHostPort("203.0.113.7", strconv.Itoa(v4port))},
		{"http://127.0.0.1:/v1", net.JoinHostPort("203.0.113.7", strconv.Itoa(v4port))},
		{"http://127.0.0.1:99999/v1", net.JoinHostPort("203.0.113.7", strconv.Itoa(v4port))},
	}
	// IPv6-pinned URL: only exercised when the host can bind a ::1 listener.
	if ln6, err6 := net.Listen("tcp", "[::1]:0"); err6 == nil {
		defer ln6.Close()
		acceptAndClose(ln6)
		urls = append(urls, struct {
			raw    string
			target string
		}{"http://::1:11434/v1", net.JoinHostPort("203.0.113.7", strconv.Itoa(ln6.Addr().(*net.TCPAddr).Port))})
	}

	ctx := context.Background()
	for _, tc := range urls {
		if !config.IsOllamaLoopback(tc.raw) {
			t.Fatalf("%q is no longer approved by the predicate: contract changed", tc.raw)
		}
		comp, err := NewOllama(Options{BaseURL: tc.raw})
		if err != nil {
			t.Fatalf("NewOllama(%q) = %v", tc.raw, err)
		}
		client := comp.(*OpenAICompat)
		tr, ok := innerTransport(client).(*http.Transport)
		if !ok || tr.DialContext == nil {
			t.Fatalf("%q: approved keyless but UNPINNED transport", tc.raw)
		}
		conn, err := tr.DialContext(ctx, "tcp", tc.target)
		if err != nil {
			t.Fatalf("%q: pinned dial to %s failed (%v): want rewrite to loopback and CONNECT", tc.raw, tc.target, err)
		}
		conn.Close()
		client.client.CloseIdleConnections()
	}
}

// acceptAndClose accepts connections in a loop and closes them immediately,
// so a pinned dial can complete the handshake against a live listener.
func acceptAndClose(ln net.Listener) {
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
}
