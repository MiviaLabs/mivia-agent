package provider

import "fmt"

// NewLLMGateway returns an LLM Gateway OpenAI-compatible completer.
// One code path serves DevPass and pay-as-you-go keys: same endpoint, same
// Bearer auth, same error envelope; the model-ID difference (DevPass rejects
// provider-prefixed IDs with 403) is gateway-side enforcement.
func NewLLMGateway(opts Options) (Completer, error) {
	return nil, fmt.Errorf("provider %q is not implemented", "llmgateway")
}
