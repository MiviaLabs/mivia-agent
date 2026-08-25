package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

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
	if err := validateBaseURL(r.BaseURL, r.ProviderName); err != nil {
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
	if err := validateSummaryOverride(r); err != nil {
		return err
	}
	return nil
}

// validateSummaryOverride enforces the [context.summary] provider/model
// override contract from ContextSummaryConfig: both keys must be set together,
// the provider must be declared under [providers], and the model must be a
// valid model identifier. The summary wiring consumes the override only after
// this gate, so a structural misconfiguration is a load error instead of a
// silently ignored key that keeps charging the session's (expensive) model for
// every compaction summary.
func validateSummaryOverride(r *Resolved) error {
	summary := r.Context.Summary
	hasProvider := summary.Provider != nil && strings.TrimSpace(*summary.Provider) != ""
	hasModel := summary.Model != nil && strings.TrimSpace(*summary.Model) != ""
	if hasProvider != hasModel {
		return fmt.Errorf("[context.summary] provider and model must be configured together")
	}
	if !hasProvider {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(*summary.Provider))
	if _, ok := r.ProviderRuntimes[provider]; !ok {
		return fmt.Errorf("[context.summary] provider %q is not a configured provider", provider)
	}
	if _, err := NormalizeModelName(*summary.Model); err != nil {
		return fmt.Errorf("[context.summary] model is invalid: %w", err)
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

// ValidateHTTPSURL rejects anything but a well-formed absolute https URL. It
// carries no per-provider or per-environment relaxation; a caller needing a
// loopback or env-var exception applies it before/after calling this.
func ValidateHTTPSURL(raw string) (*url.URL, error) {
	u, err := parseStructuralURL(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("base_url must be an absolute https URL")
	}
	return u, nil
}

// parseStructuralURL applies the length cap and structural checks shared by
// ValidateHTTPSURL and validateBaseURL's http relaxation path, without any
// scheme requirement.
func parseStructuralURL(raw string) (*url.URL, error) {
	if len(raw) > maxBaseURLLength {
		return nil, fmt.Errorf("base_url is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Fixed literal on purpose: url.Parse's error quotes the raw value,
		// and a base_url may carry credentials or control characters.
		return nil, fmt.Errorf("base_url is invalid")
	}
	if !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("base_url is invalid")
	}
	return u, nil
}

func validateBaseURL(raw, providerName string) error {
	u, err := parseStructuralURL(raw)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		return nil
	}
	switch u.Scheme {
	case "http":
		// Generalized beyond ollama: every builtin provider now gets the
		// same verified-loopback dial pinning at construction
		// (provider.NewForProvider), not just ollama's own factory, so this
		// check no longer needs to name a specific provider - the
		// protection this exception depends on is provider-agnostic.
		if IsOllamaLoopback(raw) {
			return nil
		}
		if os.Getenv("MIVIA_ALLOW_INSECURE_HTTP") == "1" {
			return nil
		}
		return fmt.Errorf("base_url must use https (set MIVIA_ALLOW_INSECURE_HTTP=1 for local http mocks)")
	default:
		return fmt.Errorf("base_url must be an absolute https URL")
	}
}
