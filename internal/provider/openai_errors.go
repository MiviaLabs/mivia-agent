package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	// not request content). The message is deliberately excluded.
	errType := ""
	if envelope.Error != nil && envelope.Error.Type != "" {
		errType = ", type " + envelope.Error.Type
	}

	switch {
	case statusCode == http.StatusOK:
		return fmt.Errorf("%s: provider error (HTTP 200%s)", "openai", errType)
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%s: rate limited (HTTP 429)", "openai")
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("%s: auth failed (HTTP %d)", "openai", statusCode)
	default:
		return fmt.Errorf("%s: provider error (HTTP %d%s)", "openai", statusCode, errType)
	}
}
