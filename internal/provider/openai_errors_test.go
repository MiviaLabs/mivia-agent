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

// --- HTTP-200 in-band error transient/permanent classification (RED) ---
//
// These tests lock the contract that a 200 response carrying an in-band error
// envelope must be classified so provider.IsTransient makes the coordinator
// step-retry layer behave: transient-class provider faults (server_error,
// internal_error, rate_limit_exceeded, overloaded_region) must be transient
// so the step retries; permanent and unknown classes must stay non-transient.
// Today openaiErrorParser's statusCode == http.StatusOK branch returns a plain
// error and never wraps TransientError nor calls markPermanent, so these
// assertion-fail (RED).

// Transient-class provider faults reported in a 200 error envelope must be
// classified transient so the coordinator step-retry layer retries them.
func TestOpenAIErrorParser200InBandTransientTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  string
	}{
		{"server_error", "server_error"},
		{"internal_error", "internal_error"},
		{"rate_limit_exceeded", "rate_limit_exceeded"},
		{"upstream_error", "upstream_error"},
		{"timeout", "timeout"},
		{"overloaded", "overloaded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"error":{"type":"` + tc.typ + `","message":"boom"}}`
			err := openaiErrorParser(http.StatusOK, []byte(body))
			if err == nil {
				t.Fatal("expected an error for in-band error")
			}
			if !IsTransient(err) {
				t.Fatalf("provider fault type %q must be transient, got IsTransient=false: %v", tc.typ, err)
			}
		})
	}
}

// When the error type is empty, classification must fall back to the code
// field (decoded as `any`). A transient overload code must be transient.
func TestOpenAIErrorParser200InBandCodeBasedClassification(t *testing.T) {
	body := `{"error":{"type":"","code":"overloaded_region"}}`
	err := openaiErrorParser(http.StatusOK, []byte(body))
	if err == nil {
		t.Fatal("expected an error for in-band error")
	}
	if !IsTransient(err) {
		t.Fatalf("code-based overload must be transient, got IsTransient=false: %v", err)
	}
}

// Permanent classes reported in a 200 error envelope must stay non-transient:
// the step-retry layer must NOT retry a call the provider refused permanently.
func TestOpenAIErrorParser200InBandPermanentTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  string
		code string
	}{
		{"invalid_request_error", "invalid_request_error", ""},
		{"model_not_found", "model_not_found", "model_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"error":{"type":"` + tc.typ + `"` + func() string {
				if tc.code == "" {
					return `}`
				}
				return `,"code":"` + tc.code + `"}`
			}() + `}`
			err := openaiErrorParser(http.StatusOK, []byte(body))
			if err == nil {
				t.Fatal("expected an error for in-band error")
			}
			if IsTransient(err) {
				t.Fatalf("permanent type %q must NOT be transient, got IsTransient=true: %v", tc.typ, err)
			}
		})
	}
}

// An unknown class fails closed: it must not be treated as transient, so the
// step-retry layer only retries faults it can positively identify.
func TestOpenAIErrorParser200InBandUnknownClassFailsClosed(t *testing.T) {
	body := `{"error":{"type":"mystery_class"}}`
	err := openaiErrorParser(http.StatusOK, []byte(body))
	if err == nil {
		t.Fatal("expected an error for in-band error")
	}
	if IsTransient(err) {
		t.Fatalf("unknown type %q must fail closed as non-transient, got IsTransient=true: %v", "mystery_class", err)
	}
}

// A 200 in-band transient error must still hide the provider's message text
// (privacy: request-echoing content must never reach err.Error()).
func TestOpenAIErrorParser200InBandTransientStripsSecret(t *testing.T) {
	body := `{"error":{"type":"server_error","message":"S3CR3T-request-echo"}}`
	err := openaiErrorParser(http.StatusOK, []byte(body))
	if err == nil {
		t.Fatal("expected an error for in-band error")
	}
	if !IsTransient(err) {
		t.Fatalf("server_error in-band must be transient: %v", err)
	}
	if strings.Contains(err.Error(), "S3CR3T-request-echo") {
		t.Fatalf("request secret leaked in error text: %v", err)
	}
}

// The non-200 path must remain unchanged: status 500 surface type info only,
// never the provider's message text, and behaves as today.
func TestOpenAIErrorParser200InBandNon200Unchanged(t *testing.T) {
	body := `{"error":{"type":"server_error","message":"S3CR3T-request-echo"}}`
	err := openaiErrorParser(http.StatusInternalServerError, []byte(body))
	if err == nil {
		t.Fatal("expected an error at HTTP 500")
	}
	if strings.Contains(err.Error(), "S3CR3T-request-echo") {
		t.Fatalf("provider message leaked at HTTP 500: %v", err)
	}
}
