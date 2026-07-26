package config

// Built-in provider defaults.
const (
	DefaultProvider = "deepseek"

	DeepSeekName         = "deepseek"
	DeepSeekDefaultModel = "deepseek-v4-flash"
	DeepSeekProModel     = "deepseek-v4-pro"
	DeepSeekDefaultURL   = "https://api.deepseek.com/v1"
	DeepSeekAPIKeyEnv    = "DEEPSEEK_API_KEY"

	OpenRouterName         = "openrouter"
	OpenRouterDefaultModel = "openai/gpt-4o-mini"
	OpenRouterDefaultURL   = "https://openrouter.ai/api/v1"
	OpenRouterAPIKeyEnv    = "OPENROUTER_API_KEY"
)

// KnownProviders lists supported provider names.
var KnownProviders = []string{DeepSeekName, OpenRouterName}
