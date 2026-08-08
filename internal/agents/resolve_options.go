package agents

import "github.com/MiviaLabs/mivia-agent/internal/config"

// ResolveOptions controls inheritance and global guardrails.
type ResolveOptions struct {
	Global             config.AgentsGlobal
	MCPConfig          config.MCPConfig
	KnownTools         map[string]struct{}
	SkillNames         map[string]struct{}
	ReservedHandlers   map[string]struct{}
	SkillCatalogue     map[string]SkillCatalogueEntry
	AllowProjectSkills bool
	TolerantWorkspace  bool
}
