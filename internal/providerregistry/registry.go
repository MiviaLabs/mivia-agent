// Package providerregistry owns dependency-neutral built-in provider metadata.
package providerregistry

import (
	"sort"
	"strings"
)

// Descriptor contains the common configuration defaults for a provider.
type Descriptor struct {
	Name             string
	DefaultModel     string
	DefaultURL       string
	DefaultAPIKeyEnv string
}

var descriptors = map[string]Descriptor{
	"deepseek": {
		Name: "deepseek", DefaultModel: "deepseek-v4-flash",
		DefaultURL: "https://api.deepseek.com/v1", DefaultAPIKeyEnv: "DEEPSEEK_API_KEY",
	},
	"llmgateway": {
		Name: "llmgateway", DefaultModel: "deepseek-v4-pro",
		DefaultURL: "https://api.llmgateway.io/v1", DefaultAPIKeyEnv: "LLMGATEWAY_API_KEY",
	},
	"llmproxycli": {
		Name: "llmproxycli", DefaultModel: "claude-sonnet-5",
		DefaultURL: "http://127.0.0.1:8317/v1", DefaultAPIKeyEnv: "CLIPROXY_API_KEY",
	},
	"minimax": {
		Name: "minimax", DefaultModel: "MiniMax-M3",
		DefaultURL: "https://api.minimax.io/v1", DefaultAPIKeyEnv: "MINIMAX_API_KEY",
	},
	"ollama": {
		Name: "ollama", DefaultModel: "gpt-oss:120b",
		DefaultURL: "https://ollama.com/v1", DefaultAPIKeyEnv: "OLLAMA_API_KEY",
	},
	"openrouter": {
		Name: "openrouter", DefaultModel: "openai/gpt-5.6-luna",
		DefaultURL: "https://openrouter.ai/api/v1", DefaultAPIKeyEnv: "OPENROUTER_API_KEY",
	},
	"zai": {
		Name: "zai", DefaultModel: "glm-5.2",
		DefaultURL: "https://api.z.ai/api/paas/v4", DefaultAPIKeyEnv: "ZAI_API_KEY",
	},
}

// Lookup returns metadata for a built-in provider.
func Lookup(name string) (Descriptor, bool) {
	d, ok := descriptors[strings.ToLower(strings.TrimSpace(name))]
	return d, ok
}

// Names returns a sorted copy of the supported provider names.
func Names() []string {
	names := make([]string, 0, len(descriptors))
	for name := range descriptors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
