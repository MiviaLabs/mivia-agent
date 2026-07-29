package config

import (
	"fmt"
	"os"
	"strings"

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

	providerName, pc, err := resolveProvider(file, opts)
	if err != nil {
		return nil, err
	}

	envMap, envPath, envUsed, err := loadEnvMap(file.Provider.EnvFile)
	if err != nil {
		return nil, err
	}

	key, keySet := envfile.Lookup(pc.APIKeyEnv, envMap)

	mct := 0
	if file.Chat.MaxContextTokens != nil {
		mct = *file.Chat.MaxContextTokens
	}
	subagentCfg := resolveSubagentConfig(file.Subagents)
	storeBackend := subagentCfg.StoreBackend
	if storeBackend == "" {
		storeBackend = "memory"
	}
	storePath := subagentCfg.StorePath
	if storeBackend == "sqlite" && storePath == "" {
		storePath = defaultStorePath()
		subagentCfg.StorePath = storePath // write back so downstream code (initCoordinator, NewSessionDispatcher) uses the resolved path
	}
	subagentCfg.StoreBackend = storeBackend // write back so downstream code can check without re-resolving
	// Compile here so a malformed pattern is a startup error naming the
	// expression, not a rule silently dropped at the first redaction call —
	// an operator who believes they are covered and is not, is worse off than
	// one whose config refuses to load.
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
		ConfigPath:       configPath,
		EnvFilePath:      envPath,
		EnvFileUsed:      envUsed,
		ProviderName:     providerName,
		Model:            pc.Model,
		BaseURL:          strings.TrimRight(pc.BaseURL, "/"),
		APIKeyEnv:        pc.APIKeyEnv,
		APIKeySet:        keySet && key != "",
		APIKey:           key,
		HTTPReferer:      pc.HTTPReferer,
		XTitle:           pc.XTitle,
		SystemPrompt:     file.Chat.SystemPrompt,
		MaxContextTokens: mct,
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
		res.ConfigPath = "(defaults)"
	}
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

func resolveProvider(file File, opts LoadOptions) (string, ProviderConfig, error) {
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
		return "", ProviderConfig{}, fmt.Errorf("unknown provider %q (supported: %s)", name, strings.Join(providerregistry.Names(), ", "))
	}
	pc := file.Providers[name]
	if pc.Model == "" {
		pc.Model = descriptor.DefaultModel
	}
	if pc.BaseURL == "" {
		pc.BaseURL = descriptor.DefaultURL
	}
	if pc.APIKeyEnv == "" {
		pc.APIKeyEnv = descriptor.DefaultAPIKeyEnv
	}
	if opts.ModelOverride != "" {
		pc.Model = opts.ModelOverride
	}
	return name, pc, nil
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
	if err := toml.Unmarshal(data, &file); err != nil {
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

// Validate checks resolved settings without requiring an API key
// (key requirement is enforced by doctor/chat).
// resolveSubagentConfig merges file config with defaults.
func resolveSubagentConfig(cfg SubagentConfig) SubagentConfig {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = DefaultSubagentConfig.MaxWorkers
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = DefaultSubagentConfig.MaxDepth
	}
	if cfg.MaxFanout <= 0 {
		cfg.MaxFanout = DefaultSubagentConfig.MaxFanout
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = DefaultSubagentConfig.DefaultTimeout
	}
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
