package provider

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
)

// NewZAI returns a ZAI GLM OpenAI-compatible completer for the standard PaaS endpoint.
//
// GLM Coding Plan keys are not served here: they need base_url set to
// https://api.z.ai/api/coding/paas/v4. Against this endpoint such a key has no
// pay-as-you-go balance and every request fails with code 1113.
func NewZAI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		descriptor, ok := providerregistry.Lookup("zai")
		if !ok {
			return nil, fmt.Errorf("provider %q has no built-in descriptor", "zai")
		}
		base = descriptor.DefaultURL
	}
	return NewOpenAICompatWithOptions(CompatOptions{
		Name:    "zai",
		BaseURL: base,
		APIKey:  opts.APIKey,
		ExtraHeaders: map[string]string{
			"Accept-Language": "en-US,en",
		},
		ErrorParser:       zaiErrorParser,
		NonRetryable:      zaiNonRetryable,
		CacheUsageEnabled: opts.CacheUsageEnabled,
		// GLM gates thinking with a thinking object. A model that also wants
		// graded depth (GLM-5.2+) names reasoning_dialect = "thinking_effort"
		// on its own entry; the factory cannot know the model.
		//
		// The dialect is read from the vetted table rather than named here
		// because config validates model entries against that same table. A
		// second spelling of it would let load accept a set this client then
		// encodes in a different shape.
		Reasoning: defaultReasoningDialect("zai"),
	}), nil
}
