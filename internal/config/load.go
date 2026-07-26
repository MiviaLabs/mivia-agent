package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/envfile"
	"github.com/pelletier/go-toml/v2"
)

// LoadOptions controls config resolution.
type LoadOptions struct {
	// ConfigPath forces a config file; empty uses search order.
	ConfigPath string
	// ProviderOverride forces provider name (CLI flag).
	ProviderOverride string
	// ModelOverride forces model (CLI flag).
	ModelOverride string
	// AllowMissingConfig uses built-in defaults when no TOML is found.
	AllowMissingConfig bool
}

// Load resolves config + env credentials.
func Load(opts LoadOptions) (*Resolved, error) {
	var (
		file       File
		configPath string
		found      bool
	)

	if opts.ConfigPath != "" {
		configPath = ExpandPath(opts.ConfigPath)
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", configPath, err)
		}
		if err := toml.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", configPath, err)
		}
		found = true
	} else {
		candidates := DefaultConfigCandidates()
		if p, ok := FirstExisting(candidates); ok {
			configPath = p
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, fmt.Errorf("read config %s: %w", p, err)
			}
			if err := toml.Unmarshal(data, &file); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", p, err)
			}
			found = true
		} else if !opts.AllowMissingConfig {
			return nil, fmt.Errorf("no config file found (tried %s); set MIVIA_CONFIG or create mivia.toml", strings.Join(candidates, ", "))
		}
	}

	if file.Providers == nil {
		file.Providers = map[string]ProviderConfig{}
	}

	providerName := strings.TrimSpace(file.Provider.Name)
	if providerName == "" {
		providerName = DefaultProvider
	}
	if opts.ProviderOverride != "" {
		providerName = strings.TrimSpace(opts.ProviderOverride)
	}
	providerName = strings.ToLower(providerName)

	pc := file.Providers[providerName]
	// Apply built-in defaults per provider.
	switch providerName {
	case DeepSeekName:
		if pc.Model == "" {
			pc.Model = DeepSeekDefaultModel
		}
		if pc.BaseURL == "" {
			pc.BaseURL = DeepSeekDefaultURL
		}
		if pc.APIKeyEnv == "" {
			pc.APIKeyEnv = DeepSeekAPIKeyEnv
		}
	case OpenRouterName:
		if pc.Model == "" {
			pc.Model = OpenRouterDefaultModel
		}
		if pc.BaseURL == "" {
			pc.BaseURL = OpenRouterDefaultURL
		}
		if pc.APIKeyEnv == "" {
			pc.APIKeyEnv = OpenRouterAPIKeyEnv
		}
	default:
		return nil, fmt.Errorf("unknown provider %q (supported: %s)", providerName, strings.Join(KnownProviders, ", "))
	}

	if opts.ModelOverride != "" {
		pc.Model = opts.ModelOverride
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
	res := &Resolved{
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
	}
	if !found {
		res.ConfigPath = "(defaults)"
	}
	if err := res.Validate(); err != nil {
		return nil, err
	}
	return res, nil
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

// Validate checks resolved settings without requiring an API key
// (key requirement is enforced by doctor/chat).
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
