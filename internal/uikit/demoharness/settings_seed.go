package demoharness

import "github.com/MiviaLabs/mivia-agent/internal/uikit/ports"

// The seed data below is read-only TEMPLATE content, cloned into each
// Harness at construction (settingsSeed), never shared or mutated in
// place - the same pattern demoModels/demoAgents already use in
// commands.go. Every credential-shaped string is an obvious fake
// ("sk-test-not-real...", "example.internal") per
// .agents/rules/10-security-privacy.md: these values ship in this
// repo's source and must never look like a real secret.

func seedGeneral() ports.GeneralView {
	return ports.GeneralView{
		Theme:                  "mivia-dark",
		Mouse:                  true,
		ShowReasoning:          true,
		ShowIterationNotices:   false,
		ShowPromptCacheNotices: false,
		ScrollLines:            3,
		ApprovalDefault:        "once",
		ScreenReader:           false,
		ReducedMotion:          false,
	}
}

func seedProviders() []ports.ProviderView {
	return []ports.ProviderView{
		{
			Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
			APIKeyEnv: "OPENROUTER_API_KEY", APIKeySet: true,
			Active: true, Selectable: true, ActiveModel: "anthropic/claude-opus-5", DefaultModel: "anthropic/claude-opus-5",
			Models: []ports.ModelView{
				{Name: "anthropic/claude-opus-5", ContextWindowTokens: 200_000, ReasoningEfforts: []string{"low", "high"}, Reasoning: "high"},
				{Name: "openai/gpt-5", ContextWindowTokens: 128_000},
			},
		},
		{
			Name: "ollama", BaseURL: "http://localhost:11434",
			APIKeyEnv: "", APIKeySet: false, Selectable: true, DefaultModel: "llama3.1",
			Models: []ports.ModelView{
				{Name: "llama3.1", ContextWindowTokens: 128_000},
			},
		},
		{
			Name: "deepseek", BaseURL: "https://api.deepseek.com",
			APIKeyEnv: "DEEPSEEK_API_KEY", APIKeySet: false,
			Selectable: false, DisabledReason: "credential unavailable",
			BuiltIn: true,
		},
	}
}

func seedMCPServers() []ports.MCPServerView {
	return []ports.MCPServerView{
		{
			ID: "filesystem", Transport: "stdio", Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "--token=sk-test-not-real-canary"},
			Enabled: true, TimeoutSeconds: 30, State: ports.MCPStateConnected, ToolCount: 6,
		},
		{
			ID: "search", Transport: "streamable_http",
			Endpoint: "https://search.example.internal/mcp",
			EnvNames: []string{"SEARCH_API_KEY"}, Enabled: true, TimeoutSeconds: 15,
			State: ports.MCPStateFailed, FailKind: ports.MCPFailAuth,
			FailMessage: "authentication failed", ToolCount: 0,
		},
	}
}

func seedAgents() []ports.AgentView {
	return []ports.AgentView{
		{Name: ports.DefaultAgentName, Description: "general purpose orchestrator", Provider: "openrouter", Model: "anthropic/claude-opus-5", MaxTurns: 40, SystemPromptChars: 4200},
		{Name: "go-engineer", Description: "implements Go changes", Provider: "openrouter", Model: "anthropic/claude-opus-5", Tools: []string{"edit_file", "run_command"}, MaxTurns: 60, SystemPromptChars: 2100},
	}
}

func seedAutomations() []ports.Automation {
	return []ports.Automation{
		{
			ID: "nightly-audit", Name: "Nightly bug audit", Description: "runs the fast bug audit workflow",
			Enabled: true,
			Trigger: ports.TriggerSpec{Kind: ports.TriggerScheduled, Schedule: &ports.ScheduleSpec{
				Kind: ports.ScheduleRecurring, Cron: "0 2 * * *", TZ: "UTC",
			}},
			Action: ports.ActionRef{Workflow: "bug-fix-fast"},
		},
		{
			ID: "manual-release-check", Name: "Release checklist", Description: "manual pre-release verification",
			Enabled: true,
			Trigger: ports.TriggerSpec{Kind: ports.TriggerManual},
			Action:  ports.ActionRef{Workflow: "feature-delivery"},
		},
	}
}
