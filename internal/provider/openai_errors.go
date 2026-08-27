package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openaiErrorEnvelope is the standard OpenAI-compatible error shape returned by
// DeepSeek, OpenRouter, and most OpenAI-compatible providers. The error field
// carries structured diagnostics that may include request-echoing content in the
// message — the same class of risk that motivated z.ai's static-code-only
// parser. This parser surfaces only the HTTP status and, when available, the
// error type — a content-free classification that never forwards the provider's
// own text.
type openaiErrorEnvelope struct {
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
	Choices json.RawMessage `json:"choices"`
}

// openaiErrorParser is the default error parser for OpenAI-compatible providers.
// It runs on every HTTP response (including individual SSE chunks) and returns a
// status-code-only error for any response that carries an error signal, without
// forwarding the provider's own message text. Providers that need richer
// diagnostics (e.g. z.ai with its numeric body codes) override this with a
// custom ErrorParser that maps content-free keys to static explanations.
//
// The parser returns nil for clean responses: HTTP 200 with no error field, or
// HTTP 200 carrying choices (a completion, including streamed chunks). This lets
// the caller continue parsing without error interception on every SSE chunk.
func openaiErrorParser(statusCode int, body []byte) error {
	var envelope openaiErrorEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		// Non-JSON body is an error for non-200, a clean pass for 200.
		if statusCode != http.StatusOK {
			return fmt.Errorf("%s: provider error (HTTP %d)", "openai", statusCode)
		}
		return nil
	}

	// A 200 carrying choices is a completion, including every streamed chunk.
	if statusCode == http.StatusOK && len(envelope.Choices) != 0 {
		return nil
	}

	// No error field and no failing status: clean.
	if envelope.Error == nil && statusCode == http.StatusOK {
		return nil
	}

	// An error field is present — report it as a provider error with only the
	// status code and, if available, the error type (a classification string,
	// not request content). The message is deliberately excluded: it is read
	// only to classify a prompt-too-long rejection (ErrPromptTooLong) or a
	// max-tokens-cap rejection (ErrMaxTokensExceeded), and its text never
	// appears in the surfaced error.
	errType := ""
	promptTooLong := false
	maxTokensCap := false
	if envelope.Error != nil {
		if envelope.Error.Type != "" {
			errType = ", type " + envelope.Error.Type
		}
		promptTooLong = isPromptTooLongMessage(envelope.Error.Message)
		maxTokensCap = isMaxTokensCapMessage(envelope.Error.Message)
	}

	switch {
	case statusCode == http.StatusOK:
		if promptTooLong {
			return fmt.Errorf("%s: provider error (HTTP 200%s): %w", "openai", errType, ErrPromptTooLong)
		}
		err := fmt.Errorf("%s: provider error (HTTP 200%s)", "openai", errType)
		// An in-band error envelope on HTTP 200 is a provider fault the HTTP
		// status cannot express. Classify it from the type (or the code when
		// the type is empty) so the coordinator step-retry layer knows whether
		// to retry: transient-class faults (server/api/timeout/upstream errors,
		// overload, rate limit) are retried, while permanent and unknown
		// classes fail closed and stay non-transient. A max-tokens-cap wording
		// attaches the clamp sentinel only for a NON-transient in-band
		// rejection; a transient-typed envelope keeps its pre-existing
		// transient classification, because a step retry is the recovery a
		// server fault earns (the clamp is a cap correction, not a fault
		// retry).
		if openai200ErrorTransient(envelope.Error.Type, envelope.Error.Code) {
			return &TransientError{Err: err}
		}
		if maxTokensCap {
			return fmt.Errorf("%s: provider error (HTTP 200%s): %w", "openai", errType, ErrMaxTokensExceeded)
		}
		return markPermanent(err)
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limited (HTTP 429)", "openai")
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("%s: auth failed (HTTP %d)", "openai", statusCode)
	default:
		if promptTooLong {
			return fmt.Errorf("%s: provider error (HTTP %d%s): %w", "openai", statusCode, errType, ErrPromptTooLong)
		}
		if maxTokensCap {
			return fmt.Errorf("%s: provider error (HTTP %d%s): %w", "openai", statusCode, errType, ErrMaxTokensExceeded)
		}
		return fmt.Errorf("%s: provider error (HTTP %d%s)", "openai", statusCode, errType)
	}
}

// openai200ErrorTransient reports whether an in-band error envelope carried on
// an HTTP 200 response names a transient-class provider fault that the
// step-retry layer should retry. It matches on the error type when present,
// and falls back to the code field (decoded as any) when the type is empty.
//
// Only positively-identified transient classes return true; permanent and
// unknown classes fail closed (false), so a call the provider refused for a
// stable reason is never re-run. The match is byte/content-free: it inspects
// the classification type/code strings only, never the provider's message.
func openai200ErrorTransient(errType string, code any) bool {
	value := errType
	if value == "" {
		s, ok := code.(string)
		if !ok {
			return false
		}
		value = s
	}
	lower := strings.ToLower(value)
	switch lower {
	case "server_error", "api_error", "timeout_error", "upstream_error", "internal_error":
		return true
	}
	// Prefix/substring transient classes: overload*, *timeout*, rate_limit*.
	if strings.HasPrefix(lower, "overloaded") ||
		strings.HasPrefix(lower, "rate_limit") ||
		strings.Contains(lower, "timeout") {
		return true
	}
	return false
}
