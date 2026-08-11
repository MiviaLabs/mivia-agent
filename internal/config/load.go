package config

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/pelletier/go-toml/v2"
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
}

// Load resolves config + env credentials.
func Load(opts LoadOptions) (*Resolved, error) {
	file, configPath, found, err := loadFile(opts)
	if err != nil {
		return nil, err
	}
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
	if err := normalizeProviderConfigs(&file, maxTokens); err != nil {
		return nil, err
	}
	mcpConfig, mcpWarnings, err := loadRuntimeMCPConfig(opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	memCfg, err := resolveMemoryConfig(file, configPath)
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
	key, keySet := envfile.Lookup(pc.APIKeyEnv, envMap)
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
		RedactionPolicy:  redactionPolicy,
		MaxSteps:         file.Chat.MaxSteps,
		ConfigPath:       configPath,
		EnvFilePath:      envPath,
		EnvFileUsed:      envUsed,
		ProviderName:     providerName,
		Model:            model,
		Models:           modelNames(pc.Models),
		ModelProfiles:    cloneModelSpecs(pc.Models),
		BaseURL:          strings.TrimRight(pc.BaseURL, "/"),
		APIKeyEnv:        pc.APIKeyEnv,
		APIKeySet:        keySet && key != "",
		APIKey:           key,
		HTTPReferer:      pc.HTTPReferer,
		XTitle:           pc.XTitle,
		SystemPrompt:     file.Chat.SystemPrompt,
		MaxPromptTokens:  file.Chat.MaxPromptTokens,
		MaxContextTokens: activePromptBudget,
		Temperature:      file.Chat.Temperature,
		MaxTokens:        file.Chat.MaxTokens,
		Subagents:        subagentCfg,
		Worktrees:        file.Worktrees,
		StoreBackend:     storeBackend,
		StorePath:        storePath,
		StorePathSet:     file.Subagents.StorePath != "",
		Privacy:          resolvePrivacyConfig(file.Privacy),
		Context:          resolveContextConfig(file.Context),
		Tools:            resolveToolsConfig(file.Tools),
		Memory:           memCfg,
		Harness:          file.Harness,
		MCP:              mcpConfig,
		MCPWarnings:      append([]string(nil), mcpWarnings...),
		TavilyAPIKey:     resolveTavilyAPIKey(file.Integrations.Tavily, envMap),
		PromptCache:      resolvePromptCache(file.Provider.PromptCache),
	}
	if !found {
		return nil, fmt.Errorf("no configured provider models available")
	}
	res.ProviderRuntimes, res.modelCatalog = resolveProviderRuntimes(file, envMap, providerName)
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return res, nil
}

// resolveSubagentStoreBackend normalizes and validates [subagents]
// store_backend like the sibling [memory] backend (resolveMemoryConfig) and
// returns the resolved store path (defaulted when sqlite has none). The
// backend is a closed enum; an unvalidated value such as "SQLite" previously
// survived to the CLI, where the exact "sqlite" equality checks silently
// selected the in-memory backend and lost orchestration history on process
// exit with no error or warning.
func resolveSubagentStoreBackend(subagentCfg SubagentConfig, configPath string) (SubagentConfig, string, error) {
	storeBackend := strings.ToLower(strings.TrimSpace(subagentCfg.StoreBackend))
	if storeBackend == "" {
		storeBackend = memory.BackendMemory
	}
	if storeBackend != memory.BackendMemory && storeBackend != memory.BackendSQLite {
		return subagentCfg, "", fmt.Errorf("config %s: [subagents] store_backend must be \"memory\" or \"sqlite\", got %q", configPath, subagentCfg.StoreBackend)
	}
	if storeBackend == memory.BackendSQLite && subagentCfg.StorePath == "" {
		subagentCfg.StorePath = defaultStorePath()
	}
	subagentCfg.StoreBackend = storeBackend
	return subagentCfg, subagentCfg.StorePath, nil
}

func loadRuntimeMCPConfig(workspaceRoot string) (MCPConfig, []string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		var err error
		workspaceRoot, err = os.Getwd()
		if err != nil {
			return MCPConfig{}, nil, fmt.Errorf("get workspace directory: %w", err)
		}
	}
	cfg, warnings, err := LoadTrustedMCPConfig(workspaceRoot)
	return cfg, warnings, err
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
	key, ok := envfile.Lookup(envName, envMap)
	if ok && key != "" {
		return key
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

const (
	maxProviderModels      = 128
	minContextWindowTokens = 1024
	maxContextWindowTokens = 10_000_000
)

func resolveProvider(file File, opts LoadOptions) (string, ProviderConfig, string, error) {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	name := strings.TrimSpace(file.Provider.Name)
	if name == "" {
		name = DefaultProvider
	}
	if opts.ProviderOverride != "" {
		name = strings.TrimSpace(opts.ProviderOverride)
	}
	name = strings.ToLower(name)
	descriptor, ok := providerregistry.Lookup(name)
	if !ok {
		return "", ProviderConfig{}, "", fmt.Errorf("unknown provider %q (supported: %s)", name, strings.Join(providerregistry.Names(), ", "))
	}
	pc := file.Providers[name]
	if len(pc.Models) == 0 {
		return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: models must be non-empty", name)
	}
	defaultModel := strings.TrimSpace(pc.DefaultModel)
	model := pc.Models[0].Name
	if defaultModel != "" {
		normalizedDefault, err := NormalizeModelName(defaultModel)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is invalid", name)
		}
		defaultModel = normalizedDefault
		if !slices.Contains(modelNames(pc.Models), normalizedDefault) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: default_model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		pc.DefaultModel = defaultModel
		model = defaultModel
	}
	if pc.BaseURL == "" {
		pc.BaseURL = descriptor.DefaultURL
	}
	if pc.APIKeyEnv == "" {
		pc.APIKeyEnv = descriptor.DefaultAPIKeyEnv
	}
	if strings.TrimSpace(opts.ModelOverride) != "" {
		override, err := NormalizeModelName(opts.ModelOverride)
		if err != nil {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is invalid", name)
		}
		if !slices.Contains(modelNames(pc.Models), override) {
			return "", ProviderConfig{}, "", fmt.Errorf("[providers.%s]: --model is not in models (%s)", name, strings.Join(modelNames(pc.Models), ", "))
		}
		model = override
	}
	return name, pc, model, nil
}

func normalizeProviderConfigs(file *File, maxTokens int) error {
	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}
	seen := make(map[string]string, len(file.Providers))
	normalized := make(map[string]ProviderConfig, len(file.Providers))
	for rawName, pc := range file.Providers {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" {
			return fmt.Errorf("provider name is empty")
		}
		if previous, ok := seen[name]; ok && previous != rawName {
			return fmt.Errorf("provider names %q and %q collide by case", previous, rawName)
		}
		if _, ok := providerregistry.Lookup(name); !ok {
			return fmt.Errorf("unknown provider %q", name)
		}
		if pc.LegacyModel != nil {
			return fmt.Errorf("[providers.%s]: model is no longer supported; declare models", name)
		}
		models, err := normalizeModels(pc.Models, maxTokens, name)
		if err != nil {
			return fmt.Errorf("[providers.%s]: %w", name, err)
		}
		pc.Models = models
		if pc.BaseURL == "" {
			d, _ := providerregistry.Lookup(name)
			pc.BaseURL = d.DefaultURL
		}
		if pc.APIKeyEnv == "" {
			d, _ := providerregistry.Lookup(name)
			pc.APIKeyEnv = d.DefaultAPIKeyEnv
		}
		normalized[name] = pc
		seen[name] = rawName
	}
	file.Providers = normalized
	return nil
}

func modelNames(models []ModelSpec) []string {
	names := make([]string, len(models))
	for i, model := range models {
		names[i] = model.Name
	}
	return names
}

func cloneModelSpecs(models []ModelSpec) []ModelSpec {
	return slices.Clone(models)
}

func loadFile(opts LoadOptions) (File, string, bool, error) {
	path := ExpandPath(opts.ConfigPath)
	if path == "" {
		path, _ = FirstExisting(DefaultConfigCandidates())
	}
	if path == "" {
		if !opts.AllowMissingConfig {
			return File{}, "", false, fmt.Errorf("no config file found (tried %s); set MIVIA_CONFIG or create .mivia/mivia.toml", strings.Join(DefaultConfigCandidates(), ", "))
		}
		return File{}, "", false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, path, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file File
	dec := toml.NewDecoder(bytes.NewReader(data)).EnableUnmarshalerInterface()
	if err := dec.Decode(&file); err != nil {
		return File{}, path, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	// Probe the raw bytes for an explicit [subagents] inline_output_bytes key:
	// the main decode cannot tell an explicit 0 from an absent key, and
	// resolveSubagentConfig must preserve an explicit 0 ("always use refs").
	probeInlineOutputBytes(data, &file)
	// Raw bytes are the only place model keys still exist; see auditModelKeys.
	if err := auditModelKeys(data); err != nil {
		return File{}, path, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, path, true, nil
}

// probeInlineOutputBytes re-parses data for an explicit [subagents]
// inline_output_bytes key and records its presence on file.Subagents. A *int
// field keeps presence (nil = absent) distinct from value (0 is a real
// "always use refs" configuration). The main decode already accepted data
// into the superset File struct, so re-unmarshalling the same bytes into this
// narrower probe struct cannot fail; the error is discarded rather than
// plumbed through as an untestable path.
func probeInlineOutputBytes(data []byte, file *File) {
	var probe struct {
		Subagents struct {
			InlineOutputBytes *int `toml:"inline_output_bytes"`
		} `toml:"subagents"`
	}
	_ = toml.Unmarshal(data, &probe)
	file.Subagents.inlineOutputBytesSet = probe.Subagents.InlineOutputBytes != nil
}

// loadSelectedWorktreeConfig reads the selected config file again to preserve
// the difference between an absent branch_prefix and an explicit empty value.
func loadSelectedWorktreeConfig(path string, found bool) (WorktreeConfig, error) {
	if !found {
		return resolveWorktreeConfig(WorktreeConfig{})
	}
	return loadWorktreeConfigPath(path)
}

func loadEnvMap(explicit string) (map[string]string, string, bool, error) {
	if explicit != "" {
		path := ExpandPath(explicit)
		m, err := envfile.Load(path)
		if err != nil {
			return nil, path, false, fmt.Errorf("load env_file %s: %w", path, err)
		}
		return m, path, true, nil
	}
	if p, ok := FirstExisting(DefaultEnvCandidates()); ok {
		m, err := envfile.Load(p)
		if err != nil {
			return nil, p, false, fmt.Errorf("load env file %s: %w", p, err)
		}
		return m, p, true, nil
	}
	return map[string]string{}, "", false, nil
}

// resolveSubagentConfig merges file config with defaults.
// Only the system prompt is defaulted; 0 means unlimited for all bounds
// (NestedSteps, MaxDepth, MaxFanout, MaxWorkers).
func resolveSubagentConfig(cfg SubagentConfig) SubagentConfig {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSubagentConfig.SystemPrompt
	}
	if !cfg.inlineOutputBytesSet && cfg.InlineOutputBytes == 0 {
		cfg.InlineOutputBytes = DefaultSubagentConfig.InlineOutputBytes
	}
	if cfg.SchemaRetryMax <= 0 { // 0 = use default 2, not "no retries"
		cfg.SchemaRetryMax = DefaultSubagentConfig.SchemaRetryMax
	} else if cfg.SchemaRetryMax > MaxSchemaRetryMax { // typo guard, see MaxSchemaRetryMax
		cfg.SchemaRetryMax = MaxSchemaRetryMax
	}
	cfg.Messaging = resolveMessagingConfig(cfg.Messaging)
	return cfg
}
