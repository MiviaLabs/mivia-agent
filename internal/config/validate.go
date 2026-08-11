package config

import (
	"fmt"
	"net/url"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

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
