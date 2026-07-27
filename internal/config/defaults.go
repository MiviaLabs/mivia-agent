package config

// Default subagent config values.
var DefaultSubagentConfig = SubagentConfig{
	MaxWorkers:     4,
	MaxDepth:       3,
	MaxFanout:      16,
	DefaultTimeout: 60,
	DefaultBudget:  0,
	PartialResults: false,
	NestedSteps:    8,
	SystemPrompt: `You are a focused sub-agent. Complete the assigned task concisely.
Report findings as structured bullet points. Do not use tools.
Reply with only the analysis results.`,
}

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
