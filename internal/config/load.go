package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

// LoadOptions controls config resolution.
type LoadOptions struct {
	ConfigPath string
	// WorkspaceRoot selects the project MCP configuration. Empty uses the
	// current working directory for backward compatibility.
	WorkspaceRoot      string
	ProviderOverride   string
	ModelOverride      string
	AllowMissingConfig bool
	// AutoBootstrapUserConfig silently writes a minimal default config to
	// UserConfigPath() (DefaultUserConfigTOML) when the normal search
	// (opts.ConfigPath, then DefaultConfigCandidates()) finds no config file
	// anywhere. It only fires when opts.ConfigPath was left empty - an
	// explicit --config/$MIVIA_CONFIG miss stays a real error (or, for
	// $MIVIA_CONFIG, falls through to the remaining candidates, unchanged
	// pre-existing behavior) rather than being silently papered over. It
	// does not require AllowMissingConfig to also be set, but callers should
	// normally set both: if HOME cannot be resolved (UserConfigPath() is
	// ""), bootstrap is a no-op and AllowMissingConfig alone then decides
	// whether that is a hard error or a found=false load.
	//
	// Defaults to false (zero value) for every existing caller; wire it to
	// true only where a config-file-writing side effect on a missing config
	// is actually wanted (currently: `mivia chat` only - see
	// internal/cli/chat/chat_command.go's runChat). Every read-only/internal/
	// test caller of config.Load keeps today's found=false behavior
	// unchanged.
	AutoBootstrapUserConfig bool
}

// Load resolves config + env credentials.
func Load(opts LoadOptions) (*Resolved, error) {
	loaded, err := loadFile(opts)
	if err != nil {
		return nil, err
	}
	file, configPath, found := loaded.File, loaded.ConfigPath, loaded.Found
	worktreeCfg, err := loadSelectedWorktreeConfig(configPath, found)
	if err != nil {
		return nil, err
	}
	file.Worktrees = worktreeCfg
	maxTokens := 0
	if file.Chat.MaxTokens != nil {
		maxTokens = *file.Chat.MaxTokens
	}
	if file.Chat.MaxContextTokens != nil {
		return nil, fmt.Errorf("[chat]: max_context_tokens is no longer supported; use max_prompt_tokens")
	}
	if file.Chat.MaxPromptTokens != nil && (*file.Chat.MaxPromptTokens <= 0 || *file.Chat.MaxPromptTokens > maxContextWindowTokens) {
		return nil, fmt.Errorf("[chat]: max_prompt_tokens is out of range")
	}
	if file.Tools.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("[tools]: max_output_bytes must not be negative")
	}
	if file.Tools.MaxListDirEntries < 0 {
		return nil, fmt.Errorf("[tools]: max_list_dir_entries must not be negative")
	}
	if err := rejectNegativeSubagentKnobs(file.Subagents); err != nil {
		return nil, err
	}
	if err := normalizeProviderConfigs(&file, maxTokens); err != nil {
		return nil, err
	}
	root := opts.WorkspaceRoot
	if strings.TrimSpace(root) == "" {
		if isProjectConfigShape(configPath) {
			root = filepath.Dir(filepath.Dir(filepath.Clean(configPath)))
		} else if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	mcpConfig, mcpWarnings, err := loadRuntimeMCPConfig(root)
	if err != nil {
		return nil, err
	}
	if err := refuseUntrustedMCPTable(loaded.BaseMCP, configPath, root, found); err != nil {
		return nil, err
	}
	projectConfigFound := ProjectConfigExists(root)
	memCfg, err := resolveMemoryConfig(file, configPath, root, projectConfigFound)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", configPath, err)
	}
	return resolveLoaded(file, configPath, found, opts, mcpConfig, mcpWarnings, memCfg)
}

func resolveLoaded(file File, configPath string, found bool, opts LoadOptions, mcpConfig MCPConfig, mcpWarnings []string, memCfg MemoryConfig) (*Resolved, error) {
	providerName, pc, model, err := resolveProvider(file, opts)
	if err != nil {
		return nil, err
	}
	envMap, envPath, envUsed, err := loadEnvMap(file.EnvFile)
	if err != nil {
		return nil, err
	}
	key, keySet := Lookup(pc.APIKeyEnv, envMap)
	activeProfile := activeModelProfile(pc, model)
	activePromptBudget := EffectivePromptTokens(activeProfile, file.Chat.MaxTokens, promptCap(file.Chat.MaxPromptTokens), 0)
	subagentCfg, storePath, err := resolveSubagentStoreBackend(resolveSubagentConfig(file.Subagents), configPath)
	if err != nil {
		return nil, err
	}
	storeBackend := subagentCfg.StoreBackend
	redactionPolicy, err := redact.Compile(
		file.Privacy.RedactionPatterns,
		file.Privacy.RedactionKeyNames,
		file.Privacy.RedactionPlaceholder,
	)
	if err != nil {
		return nil, fmt.Errorf("config %s: [privacy]: %w", configPath, err)
	}
	res := &Resolved{
		RedactionPolicy:         redactionPolicy,
		MaxSteps:                file.Chat.MaxSteps,
		MaxUnactedContinuations: resolveUnactedContinuations(file.Chat.MaxUnactedContinuations),
		ConfigPath:              configPath,
		EnvFilePath:             envPath,
		EnvFileUsed:             envUsed,
		ProviderName:            providerName,
		Model:                   model,
		Models:                  modelNames(pc.Models),
		ModelProfiles:           cloneModelSpecs(pc.Models),
		BaseURL:                 strings.TrimRight(pc.BaseURL, "/"),
		APIKeyEnv:               pc.APIKeyEnv,
		APIKeySet:               keySet && strings.TrimSpace(key) != "",
		APIKey:                  key,
		HTTPReferer:             pc.HTTPReferer,
		XTitle:                  pc.XTitle,
		SystemPrompt:            file.Chat.SystemPrompt,
		MaxPromptTokens:         file.Chat.MaxPromptTokens,
		MaxContextTokens:        activePromptBudget,
		Temperature:             file.Chat.Temperature,
		MaxTokens:               file.Chat.MaxTokens,
		ShowIterationNotices:    file.Chat.ShowIterationNotices != nil && *file.Chat.ShowIterationNotices,
		ShowPromptCacheNotices:  file.Chat.ShowPromptCacheNotices != nil && *file.Chat.ShowPromptCacheNotices,
		Subagents:               subagentCfg,
		Worktrees:               file.Worktrees,
		StoreBackend:            storeBackend,
		StorePath:               storePath,
		StorePathSet:            file.Subagents.StorePath != "",
		Privacy:                 resolvePrivacyConfig(file.Privacy),
		Context:                 resolveContextConfig(file.Context),
		Tools:                   resolveToolsConfig(file.Tools),
		Memory:                  memCfg,
		Harness:                 file.Harness,
		Approvals:               file.Approvals,
		TUI:                     file.TUI,
		Workflows:               file.Workflows,
		Verifiers:               cloneVerifierProfiles(file.Verifiers),
		MCP:                     mcpConfig,
		MCPWarnings:             append([]string(nil), mcpWarnings...),
		TavilyAPIKey:            resolveTavilyAPIKey(file.Integrations.Tavily, envMap),
		PromptCache:             resolvePromptCache(file.Provider.PromptCache),
		Sync:                    ResolveSyncConfig(file.Sync),
	}
	applyTimeoutBudgets(res, file, subagentCfg)
	if !found {
		return nil, fmt.Errorf("no configured provider models available")
	}
	res.ProviderRuntimes, res.modelCatalog = resolveProviderRuntimes(file, envMap, providerName)
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return res, nil
}

const DefaultTavilyAPIKeyEnv = "TAVILY_API_KEY"

func resolveTavilyAPIKey(tc TavilyConfig, envMap map[string]string) string {
	envName := tc.APIKeyEnv
	if envName == "" {
		envName = DefaultTavilyAPIKeyEnv
	}
	// Disabled explicitly
	if tc.Disable {
		return ""
	}
	key, ok := Lookup(envName, envMap)
	if ok && strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	return ""
}

// resolvePromptCache defaults an unset [provider] prompt_cache to "auto" so
// a config written before this field existed keeps loading unchanged.
// Anything else passes through unchanged for Resolved.Validate to reject.
func resolvePromptCache(raw string) string {
	if raw == "" {
		return "auto"
	}
	return raw
}

// MaxUnactedContinuationsCeiling clamps [chat] max_unacted_continuations.
// Every continuation is a full extra provider call on a turn that already
// answered, so a mistyped 500 must not turn one turn into 500 billable
// requests. The sibling empty-response retry is a compiled constant for the
// same reason; this knob is configurable but still bounded.
const MaxUnactedContinuationsCeiling = 3

// resolveUnactedContinuations normalizes the configured continuation bound:
// negative or zero is off, anything above the ceiling is clamped to it.
func resolveUnactedContinuations(configured int) int {
	if configured <= 0 {
		return 0
	}
	return min(configured, MaxUnactedContinuationsCeiling)
}

func loadEnvMap(explicit string) (map[string]string, string, bool, error) {
	if explicit != "" {
		path := ExpandPath(explicit)
		m, err := sdkenvfile.Load(path)
		if err != nil {
			return nil, path, false, fmt.Errorf("load env_file %s: %w", path, err)
		}
		return m, path, true, nil
	}
	if p, ok := FirstExisting(DefaultEnvCandidates()); ok {
		m, err := sdkenvfile.Load(p)
		if err != nil {
			return nil, p, false, fmt.Errorf("load env file %s: %w", p, err)
		}
		return m, p, true, nil
	}
	return map[string]string{}, "", false, nil
}
