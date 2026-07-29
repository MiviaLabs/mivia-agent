package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewZAI returns a ZAI GLM OpenAI-compatible completer for the standard PaaS endpoint.
func NewZAI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, _ := providerregistry.Lookup("zai")
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:    "zai",
		BaseURL: base,
		APIKey:  opts.APIKey,
		ExtraHeaders: map[string]string{
			"Accept-Language": "en-US,en",
		},
		ErrorParser: func(statusCode int, body []byte) error {
			return zaiErrorParserWithAPIKey(statusCode, body, opts.APIKey)
		},
	}), nil
}

func zaiErrorParser(statusCode int, body []byte) error {
	return zaiErrorParserWithAPIKey(statusCode, body, "")
}

func zaiErrorParserWithAPIKey(statusCode int, body []byte, apiKey string) error {
	var envelope struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) != 0 || len(envelope.Code) == 0 || bytes.Equal(envelope.Code, []byte("null")) || strings.TrimSpace(envelope.Message) == "" {
		return nil
	}
	message := envelope.Message
	if apiKey != "" {
		message = string(bytes.ReplaceAll([]byte(message), []byte(apiKey), []byte("[redacted]")))
	}
	code := sanitizeErr(string(envelope.Code))
	message = sanitizeErr(message)
	return fmt.Errorf("zai: HTTP %d (code %s): %s", statusCode, code, message)
}
