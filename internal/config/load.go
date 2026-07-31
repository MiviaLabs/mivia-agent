package config

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
	"github.com/MiviaLabs/mivia-agent/internal/providerregistry"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/pelletier/go-toml/v2"
)

// LoadOptions controls config resolution.
type LoadOptions struct {
	ConfigPath         string
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
	return resolveLoaded(file, configPath, found, opts)
}

func resolveLoaded(file File, configPath string, found bool, opts LoadOptions) (*Resolved, error) {
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
	storePath := subagentCfg.StorePath
	if storeBackend == "sqlite" && storePath == "" {
		storePath = defaultStorePath()
		subagentCfg.StorePath = storePath
	}
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
		StoreBackend:     storeBackend,
		StorePath:        storePath,
		Privacy:          resolvePrivacyConfig(file.Privacy),
		Tools:            resolveToolsConfig(file.Tools),
		TavilyAPIKey:     resolveTavilyAPIKey(file.Integrations.Tavily, envMap),
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

const (
	maxProviderModels      = 128
	minContextWindowTokens = 1024
	maxContextWindowTokens = 10_000_000
)

// NormalizeModelName canonicalizes a model identifier accepted from config,
// flags, slash commands, or persisted sessions. The error deliberately omits
// the supplied value because model identifiers reach terminal output.
func NormalizeModelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("model name is empty")
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("model name is invalid")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("model name is invalid")
	}
	return name, nil
}

func normalizeModels(in []ModelSpec, maxTokens int) ([]ModelSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxProviderModels {
		return nil, fmt.Errorf("models has too many entries")
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]ModelSpec, 0, len(in))
	for i, model := range in {
		name, err := NormalizeModelName(model.Name)
		if err != nil {
			if strings.TrimSpace(model.Name) == "" {
				return nil, fmt.Errorf("models[%d] is empty", i)
			}
			return nil, fmt.Errorf("models[%d] is invalid", i)
		}
		if model.ContextWindowTokens < minContextWindowTokens || model.ContextWindowTokens > maxContextWindowTokens {
			return nil, fmt.Errorf("models[%d] has invalid context window", i)
		}
		if maxTokens <= 0 {
			return nil, fmt.Errorf("max_tokens must be positive for configured models")
		}
		if model.ContextWindowTokens <= maxTokens {
			return nil, fmt.Errorf("models[%d] context window is too small for max_tokens", i)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("models[%d] is a duplicate", i)
		}
		seen[name] = struct{}{}
		model.Name = name
		out = append(out, model)
	}
	return out, nil
}

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
		models, err := normalizeModels(pc.Models, maxTokens)
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
	return file, path, true, nil
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

func resolvePrivacyConfig(p PrivacyConfig) PrivacyConfig {
	if v, ok := os.LookupEnv("MIVIA_REDACT_TOOL_ARGS"); ok {
		p.RedactToolArgs = parseTruthyEnv(v)
	}
	return p
}

func parseTruthyEnv(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}

// resolveSubagentConfig merges file config with defaults.
// Only the system prompt is defaulted; 0 means unlimited for all bounds
// (NestedSteps, MaxDepth, MaxFanout, MaxWorkers).
func resolveSubagentConfig(cfg SubagentConfig) SubagentConfig {
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSubagentConfig.SystemPrompt
	}
	return cfg
}

// resolveToolsConfig merges TOML tool config with built-in defaults.
func resolveToolsConfig(tc ToolsConfig) ToolsConfig {
	def := DefaultToolsConfig
	if tc.RunTimeoutSec <= 0 {
		tc.RunTimeoutSec = def.RunTimeoutSec
	}
	if tc.MaxReadBytes <= 0 {
		tc.MaxReadBytes = def.MaxReadBytes
	}
	if tc.MaxWriteKB <= 0 {
		tc.MaxWriteKB = def.MaxWriteKB
	}
	if tc.MaxOutputBytes <= 0 {
		tc.MaxOutputBytes = def.MaxOutputBytes
	}
	if tc.MaxListDirEntries <= 0 {
		tc.MaxListDirEntries = def.MaxListDirEntries
	}
	// No defaulting: 0 means uncapped. Negative is normalized to 0 so every
	// consumer can treat <=0 uniformly as "no cap".
	if tc.MaxToolResultBytes < 0 {
		tc.MaxToolResultBytes = 0
	}
	// Unlike MaxToolResultBytes there is no "uncapped" state: the tools that
	// read Tavily responses declare this number as their result budget, and an
	// undeclared budget is exactly what the dispatcher's backstop destroys.
	if tc.MaxTavilyResponseBytes <= 0 {
		tc.MaxTavilyResponseBytes = def.MaxTavilyResponseBytes
	}
	// B7: RunAllowlist + RunAllowlistOnly are mutually exclusive — prefer RunAllowlistOnly
	if len(tc.RunAllowlist) > 0 && len(tc.RunAllowlistOnly) > 0 {
		tc.RunAllowlist = nil
	}
	// B7: EnvAllowlist + EnvAllowlistOnly are mutually exclusive — prefer EnvAllowlistOnly
	if len(tc.EnvAllowlist) > 0 && len(tc.EnvAllowlistOnly) > 0 {
		tc.EnvAllowlist = nil
	}
	return tc
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
	if err := validateBaseURL(r.BaseURL); err != nil {
		return err
	}
	// A positive cap below 1024 bytes starves every tool envelope (error
	// strings, JSON framing) and yields useless truncated stubs; reject it
	// rather than let the loop silently destroy every result.
	if r.Tools.MaxToolResultBytes > 0 && r.Tools.MaxToolResultBytes < 1024 {
		return fmt.Errorf("[tools] max_tool_result_bytes must be 0 (uncapped) or >= 1024, got %d", r.Tools.MaxToolResultBytes)
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
	return nil
}

func validateBaseURL(raw string) error {
	allowInsecure := os.Getenv("MIVIA_ALLOW_INSECURE_HTTP") == "1"
	if strings.HasPrefix(raw, "https://") {
		return nil
	}
	if allowInsecure && strings.HasPrefix(raw, "http://") {
		return nil
	}
	if strings.HasPrefix(raw, "http://") {
		return fmt.Errorf("base_url must use https (set MIVIA_ALLOW_INSECURE_HTTP=1 for local http mocks)")
	}
	return fmt.Errorf("base_url must be an absolute https URL")
}
