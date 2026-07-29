package provider

import (
	"encoding/json"
	"fmt"

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
		ErrorParser: zaiErrorParser,
	}), nil
}

func zaiErrorParser(statusCode int, body []byte) error {
	var envelope struct {
		Choices json.RawMessage `json:"choices"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		if statusCode != 200 {
			return fmt.Errorf("zai: provider error (HTTP %d)", statusCode)
		}
		return nil
	}
	if statusCode == 200 && len(envelope.Choices) != 0 {
		return nil
	}
	if len(envelope.Code) != 0 && envelope.Message != "" {
		var code int
		if json.Unmarshal(envelope.Code, &code) == nil {
			return fmt.Errorf("zai: provider error (HTTP %d, code %d)", statusCode, code)
		}
	}
	if len(envelope.Error) != 0 || statusCode != 200 {
		return fmt.Errorf("zai: provider error (HTTP %d)", statusCode)
	}
	return nil
}
