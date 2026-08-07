package provider

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// openaiErrorParser is the default error parser for all OpenAI-compatible
// providers. It replaces the old sanitizeErr forwarding: provider message
// text never reaches the error string. These tests mirror zai_error_test.go's
// privacy-first approach, adapted for the OpenAI envelope shape.

// --- openaiErrorParser tests ---

// A clean completion (200 with choices) is not an error, even with an error
// field alongside choices.
func TestOpenAIErrorParserPassesCleanCompletions(t *testing.T) {
	for _, body := range []struct {
		name string
		body string
	}{
		{"choices with delta", `{"choices":[{"delta":{"content":"ok"}}]}`},
		{"full completion", `{"id":"x","choices":[{"message":{"role":"assistant","content":"hello"}}]}`},
		{"empty choices with usage", `{"choices":[],"usage":{"total_tokens":5}}`},
		{"id and created only", `{"id":"x","created":1730000000}`},
		{"empty object", `{}`},
		{"empty string", ``},
	} {
		t.Run(body.name, func(t *testing.T) {
			if err := openaiErrorParser(http.StatusOK, []byte(body.body)); err != nil {
				t.Fatalf("clean payload %q rejected: %v", body.body, err)
			}
		})
	}
}

// A response with an error field but also choices is a completion (some
// providers include both on success). The error field is ignored.
func TestOpenAIErrorParserChoicesOverrideError(t *testing.T) {
	body := `{"choices":[{"delta":{"content":"ok"}}],"error":{"type":"something","message":"ignore me"}}`
	if err := openaiErrorParser(http.StatusOK, []byte(body)); err != nil {
		t.Fatalf("200 with choices rejected: %v", err)
	}
}

// Provider error bodies at non-200 status are errors. The provider message
// is never forwarded.
func TestOpenAIErrorParserReportsErrorsWithoutMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   []string // substrings that must appear in the error
		ban    []string // substrings that must NOT appear
	}{
		"400 invalid request": {
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","message":"Model gpt-5-turbo does not exist","code":"model_not_found"}}`,
			want:   []string{"HTTP 400", "type invalid_request_error"},
			ban:    []string{"gpt-5-turbo", "does not exist"},
		},
		"401 auth failure": {
			status: http.StatusUnauthorized,
			body:   `{"error":{"type":"invalid_api_key","message":"Incorrect API key provided: sk-XXXX"}}`,
			want:   []string{"auth failed", "HTTP 401"},
			ban:    []string{"sk-XXXX", "Incorrect API key"},
		},
		"402 insufficient credits": {
			status: http.StatusPaymentRequired,
			body:   `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`,
			want:   []string{"HTTP 402", "type insufficient_quota"},
			ban:    []string{"You exceeded", "current quota"},
		},
		"429 rate limited": {
			status: http.StatusTooManyRequests,
			body:   `{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`,
			want:   []string{"rate limited", "HTTP 429"},
			ban:    []string{"Rate limit reached"},
		},
		"500 server error": {
			status: http.StatusInternalServerError,
			body:   `{"error":{"type":"server_error","message":"Internal server error"}}`,
			want:   []string{"HTTP 500", "type server_error"},
			ban:    []string{"Internal server error"},
		},
		"502 bad gateway": {
			status: http.StatusBadGateway,
			body:   `{"error":{"type":"upstream_error","message":"Bad gateway"}}`,
			want:   []string{"HTTP 502"},
			ban:    []string{"Bad gateway"},
		},
		"503 overloaded": {
			status: http.StatusServiceUnavailable,
			body:   `{"error":{"type":"overloaded","message":"The server is overloaded"}}`,
			want:   []string{"HTTP 503", "type overloaded"},
			ban:    []string{"The server is overloaded"},
		},
		"non-JSON body": {
			status: http.StatusBadRequest,
			body:   `not json at all`,
			want:   []string{"HTTP 400"},
			ban:    []string{"not json"},
		},
		"empty error object": {
			status: http.StatusBadRequest,
			body:   `{"error":{}}`,
			want:   []string{"HTTP 400"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := openaiErrorParser(tc.status, []byte(tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("expected %q in error, got: %v", w, err)
				}
			}
			for _, b := range tc.ban {
				if strings.Contains(err.Error(), b) {
					t.Fatalf("provider text %q leaked in error: %v", b, err)
				}
			}
		})
	}
}

// A prompt-too-long rejection must wrap ErrPromptTooLong so the agent loop can
// compact and retry once. The provider's message is read ONLY to classify (via
// isPromptTooLongMessage); its text must never appear in err.Error().
func TestOpenAIErrorParserWrapsPromptTooLongSentinel(t *testing.T) {
	body := `{"error":{"message":"This model's maximum context length is 128000 tokens. You requested 612000 tokens. Please reduce the length...","type":"invalid_request_error"}}`
	err := openaiErrorParser(http.StatusBadRequest, []byte(body))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("expected ErrPromptTooLong, got: %v", err)
	}
	for _, banned := range []string{"maximum context length", "128000", "612000", "Please reduce"} {
		if strings.Contains(err.Error(), banned) {
			t.Fatalf("provider message %q leaked in error: %v", banned, err)
		}
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "invalid_request_error") {
		t.Fatalf("content-free classification missing: %v", err)
	}
}

// The HTTP-200 in-band error branch classifies the same way, still without
// leaking the provider's message text.
func TestOpenAIErrorParserInBandPromptTooLongWrapsSentinel(t *testing.T) {
	body := `{"error":{"type":"invalid_request_error","message":"your prompt exceeds the context length limit, reduce the prompt"}}`
	err := openaiErrorParser(http.StatusOK, []byte(body))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("expected ErrPromptTooLong, got: %v", err)
	}
	if strings.Contains(err.Error(), "exceeds the context length limit") {
		t.Fatalf("provider message leaked: %v", err)
	}
}

// A plain bad request (e.g. an unknown parameter) must NOT wrap the sentinel:
// only a classified prompt-too-long message earns the compact-and-retry path.
func TestOpenAIErrorParserPlainBadRequestDoesNotWrapPromptTooLong(t *testing.T) {
	err := openaiErrorParser(http.StatusBadRequest, []byte(`{"error":{"message":"unknown parameter","type":"invalid_request_error"}}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrPromptTooLong) {
		t.Fatalf("plain bad request must not wrap ErrPromptTooLong: %v", err)
	}
}

// OpenRouter's metadata.raw field carries the raw upstream provider error,
// which can echo request content (prompt text, API keys). The envelope parser
// must structurally ignore metadata so it never appears in an error string.
func TestOpenAIErrorParserIgnoresMetadataRaw(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   []string
		ban    []string
	}{
		"openrouter metadata.raw prompt echo": {
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":429,"message":"Rate limit","metadata":{"raw":"upstream: user said RECURSION_SECRET and API_KEY_SK1234"}}}`,
			want:   []string{"rate limited", "HTTP 429"},
			ban:    []string{"RECURSION_SECRET", "API_KEY_SK1234", "upstream: user said"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := openaiErrorParser(tc.status, []byte(tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("expected %q in error, got: %v", w, err)
				}
			}
			for _, b := range tc.ban {
				if strings.Contains(err.Error(), b) {
					t.Fatalf("provider text %q leaked in error: %v", b, err)
				}
			}
		})
	}
}

// A 200 response with an error field (in-band SSE error) is surfaced without
// the provider message. This is how OpenRouter reports mid-stream upstream failures.
func TestOpenAIErrorParserInBandErrorStripsMessage(t *testing.T) {
	// Simulate a provider that echoes the user prompt in the error message.
	prompt := "Write a haiku about recursion"
	body := `{"error":{"type":"upstream_error","message":"upstream provider failed while processing: ` + prompt + `"}}`
	err := openaiErrorParser(http.StatusOK, []byte(body))
	if err == nil {
		t.Fatal("expected an error for in-band error")
	}
	if strings.Contains(err.Error(), prompt) {
		t.Fatalf("prompt leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("expected HTTP 200 in error: %v", err)
	}
}

// An empty error object at 200 is still an error signal — the provider
// explicitly included an error field, even if the message is empty.
func TestOpenAIErrorParserEmptyErrorAt200IsError(t *testing.T) {
	if err := openaiErrorParser(http.StatusOK, []byte(`{"error":{}}`)); err == nil {
		t.Fatal("empty error at 200 should be an error")
	}
}

// The parser is set as the default for all OpenAI-compatible providers.
func TestOpenAICompatGetsDefaultErrorParser(t *testing.T) {
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: "https://example.invalid/v1", APIKey: "k"})
	if c.errorParser == nil {
		t.Fatal("expected default error parser to be set")
	}
}

// The default parser is also installed in the retry constructor.
func TestOpenAICompatWithRetryGetsDefaultErrorParser(t *testing.T) {
	c := NewOpenAICompatWithOptionsAndRetry(CompatOptions{Name: "test", BaseURL: "https://example.invalid/v1", APIKey: "k"}, nil)
	if c.errorParser == nil {
		t.Fatal("expected default error parser in retry constructor")
	}
}

// z.ai overrides the default with its own parser.
func TestZAIOverridesDefaultErrorParser(t *testing.T) {
	c, err := NewZAI(Options{BaseURL: "https://example.invalid/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	zaiCompat, ok := c.(*OpenAICompat)
	if !ok {
		t.Fatal("z.ai should be an *OpenAICompat")
	}
	if zaiCompat.errorParser == nil {
		t.Fatal("z.ai should have an error parser")
	}
	// The z.ai parser and the default parser must differ: z.ai's handles
	// numeric body codes and never returns "openai" as the provider name.
	zaiErr := zaiCompat.errorParser(http.StatusTooManyRequests, []byte(`{"error":{"code":"1113","message":"balance gone"}}`))
	defaultErr := openaiErrorParser(http.StatusTooManyRequests, []byte(`{"error":{"code":"1113","message":"balance gone"}}`))
	if zaiErr == nil || defaultErr == nil {
		t.Fatal("both should return an error")
	}
	if zaiErr.Error() == defaultErr.Error() {
		t.Fatalf("z.ai parser should differ from default:\nzai:     %v\ndefault: %v", zaiErr, defaultErr)
	}
	if !strings.Contains(zaiErr.Error(), "1113") {
		t.Fatalf("z.ai parser should surface the code: %v", zaiErr)
	}
}

// DeepSeek and OpenRouter get the default parser via NewOpenAICompatWithOptions.
func TestDeepSeekAndOpenRouterGetDefaultParser(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() (Completer, error)
	}{
		{"deepseek", func() (Completer, error) {
			return NewDeepSeek(Options{BaseURL: "https://example.invalid/v1", APIKey: "k"})
		}},
		{"openrouter", func() (Completer, error) {
			return NewOpenRouter(Options{BaseURL: "https://example.invalid/v1", APIKey: "k"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := tc.fn()
			if err != nil {
				t.Fatal(err)
			}
			compat, ok := c.(*OpenAICompat)
			if !ok {
				t.Fatalf("%s: not an *OpenAICompat", tc.name)
			}
			if compat.errorParser == nil {
				t.Fatalf("%s: expected default error parser", tc.name)
			}
		})
	}
}
