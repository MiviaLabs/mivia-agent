package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
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
	activeProfile := ModelSpec{Name: model, ContextWindowTokens: maxContextWindowTokens}
	for _, profile := range pc.Models {
		if profile.Name == model {
			activeProfile = profile
			break
		}
	}
	activePromptBudget := EffectivePromptTokens(activeProfile, file.Chat.MaxTokens, promptCap(file.Chat.MaxPromptTokens), 0)
	subagentCfg := resolveSubagentConfig(file.Subagents)
	storeBackend := subagentCfg.StoreBackend
	if storeBackend == "" {
		storeBackend = "memory"
	}
	if storeBackend == "sqlite" && subagentCfg.StorePath == "" {
		subagentCfg.StorePath = defaultStorePath()
	}
	storePath := subagentCfg.StorePath
	subagentCfg.StoreBackend = storeBackend
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
	// Raw bytes are the only place model keys still exist; see auditModelKeys.
	if err := auditModelKeys(data); err != nil {
		return File{}, path, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	return file, path, true, nil
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
	if cfg.InlineOutputBytes == 0 {
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

// resolveMessagingConfig fills zero fields with DefaultMessagingConfig.
// Messaging is always enabled; any TOML enabled= value is ignored.
func resolveMessagingConfig(cfg MessagingConfig) MessagingConfig {
	// Drop any kill-switch value so callers never observe Enabled=false.
	cfg.Enabled = nil
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = DefaultMessagingConfig.MaxBodyBytes
	}
	if cfg.MaxMessagesPerTask == 0 {
		cfg.MaxMessagesPerTask = DefaultMessagingConfig.MaxMessagesPerTask
	}
	if cfg.MailboxCapacity == 0 {
		cfg.MailboxCapacity = DefaultMessagingConfig.MailboxCapacity
	}
	if cfg.MaxPendingQuestions == 0 {
		cfg.MaxPendingQuestions = DefaultMessagingConfig.MaxPendingQuestions
	}
	// nil = default (300s); an explicit 0 is meaningful (watchdog disabled)
	// and must not be overwritten.
	if cfg.SteerWatchdogSeconds == nil {
		cfg.SteerWatchdogSeconds = intPtr(*DefaultMessagingConfig.SteerWatchdogSeconds)
	}
	cfg.Routing = resolveMessagingRouting(cfg.Routing)
	return cfg
}

func resolveMessagingRouting(cfg MessagingRoutingConfig) MessagingRoutingConfig {
	if cfg.Mode == "" {
		cfg.Mode = DefaultMessagingConfig.Routing.Mode
	}
	if cfg.MaxAsksPerTask == 0 {
		cfg.MaxAsksPerTask = DefaultMessagingConfig.Routing.MaxAsksPerTask
	}
	if cfg.MaxReferralDepth == 0 {
		cfg.MaxReferralDepth = DefaultMessagingConfig.Routing.MaxReferralDepth
	}
	if cfg.MaxReferralSpawnsPerRun == 0 {
		cfg.MaxReferralSpawnsPerRun = DefaultMessagingConfig.Routing.MaxReferralSpawnsPerRun
	}
	return cfg
}

func (r *Resolved) Validate() error {
	if r.ProviderName == "" {
		return fmt.Errorf("provider name is empty")
	}
	if r.Model == "" {
		return fmt.Errorf("model is empty")
	}
	if r.BaseURL == "" {
		return fmt.Errorf("base_url is empty")
	}
	if r.APIKeyEnv == "" {
		return fmt.Errorf("api_key_env is empty")
	}
	if !validEnvName(r.APIKeyEnv) {
		return fmt.Errorf("api_key_env is invalid")
	}
	if err := validateBaseURL(r.BaseURL); err != nil {
		return err
	}
	if _, err := secretpath.New(r.Tools.SecretPathPatterns, r.Tools.SecretPathExceptions); err != nil {
		return err
	}
	if err := validateTools(r.Tools); err != nil {
		return err
	}
	// resolveToolsConfig has already turned <= 0 into the default, so anything
	// out of range here was set deliberately. Both ends matter: below the floor
	// every Tavily response is refused, and above the ceiling the dispatcher's
	// budget + allowance + slack arithmetic can overflow int and silently
	// restore the very destruction defect the bound exists to close.
	if v := r.Tools.MaxTavilyResponseBytes; v < MinTavilyResponseBytes || v > MaxTavilyResponseLimit {
		return fmt.Errorf("[tools] max_tavily_response_bytes must be 0 (use the default) or between %d and %d, got %d",
			MinTavilyResponseBytes, MaxTavilyResponseLimit, v)
	}
	if r.PromptCache != "auto" && r.PromptCache != "off" {
		return fmt.Errorf("[provider] prompt_cache must be \"auto\" or \"off\", got %q", r.PromptCache)
	}
	return nil
}

func validEnvName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if i == 0 {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_') {
				return false
			}
			continue
		}
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// maxBaseURLLength bounds provider base_url. Every shipped and real-world
// base_url is well under 200 bytes; the cap exists so a huge-but-well-formed
// value cannot slip past the structural checks below.
const maxBaseURLLength = 8 << 10

func validateBaseURL(raw string) error {
	if len(raw) > maxBaseURLLength {
		return fmt.Errorf("base_url is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Fixed literal on purpose: url.Parse's error quotes the raw value,
		// and a base_url may carry credentials or control characters.
		return fmt.Errorf("base_url is invalid")
	}
	if !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("base_url is invalid")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if os.Getenv("MIVIA_ALLOW_INSECURE_HTTP") == "1" {
			return nil
		}
		return fmt.Errorf("base_url must use https (set MIVIA_ALLOW_INSECURE_HTTP=1 for local http mocks)")
	default:
		return fmt.Errorf("base_url must be an absolute https URL")
	}
}
