package config

import (
	"strings"
	"testing"
)

// TestResolvedValidateRejectsEmptyRequiredFields pins each of Validate's
// required-field checks: provider name, model, base_url, and api_key_env
// must each independently reject an empty value.
func TestResolvedValidateRejectsEmptyRequiredFields(t *testing.T) {
	base := Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY"}
	tests := []struct {
		name   string
		mutate func(*Resolved)
		want   string
	}{
		{"empty provider name", func(r *Resolved) { r.ProviderName = "" }, "provider name is empty"},
		{"empty model", func(r *Resolved) { r.Model = "" }, "model is empty"},
		{"empty base_url", func(r *Resolved) { r.BaseURL = "" }, "base_url is empty"},
		{"empty api_key_env", func(r *Resolved) { r.APIKeyEnv = "" }, "api_key_env is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := base
			tt.mutate(&res)
			err := res.Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestValidEnvNameRejectsOutOfRangeAndBadFirstChar pins the two failure
// branches of validEnvName that TestResolvedValidateRejectsUnsafeAPIKeyEnvironmentName
// does not reach: a name over the 128-byte cap, and a name whose first
// character is not a letter or underscore (digits are valid afterward, but
// not as the first character).
func TestValidEnvNameRejectsOutOfRangeAndBadFirstChar(t *testing.T) {
	tooLong := ""
	for i := 0; i < 129; i++ {
		tooLong += "A"
	}
	tests := []struct {
		name string
		env  string
	}{
		{"over length cap", tooLong},
		{"digit as first character", "1FOO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if validEnvName(tt.env) {
				t.Fatalf("validEnvName(%q) = true, want false", tt.env)
			}
		})
	}
}

// TestValidateHTTPSURL pins ValidateHTTPSURL's strict https-only structural
// checks: it accepts a well-formed absolute https URL and returns the parsed
// *url.URL, and rejects the same structural defects validateBaseURL rejects
// (missing scheme, http scheme, malformed URL, userinfo, fragment) with no
// ollama-loopback or MIVIA_ALLOW_INSECURE_HTTP relaxation.
func TestValidateHTTPSURL(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "")
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid https URL", "https://example.test/v1", false},
		{"missing scheme", "example.test/v1", true},
		{"http scheme rejected", "http://example.test/v1", true},
		{"ollama loopback http still rejected", "http://127.0.0.1:11434/v1", true},
		{"malformed URL", "https://%zz", true},
		{"URL with userinfo", "https://user:pass@example.test", true},
		{"URL with fragment", "https://example.test/v1#frag", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := ValidateHTTPSURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateHTTPSURL(%q) = %v, nil, want error", tt.raw, u)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateHTTPSURL(%q) = %v, %v, want nil error", tt.raw, u, err)
			}
			if u == nil || u.String() != tt.raw {
				t.Fatalf("ValidateHTTPSURL(%q) returned %v, want parsed URL for %q", tt.raw, u, tt.raw)
			}
		})
	}
}

// TestValidateBaseURLOllamaLoopbackRelaxation pins the planned ollama
// loopback relaxation: an http loopback base_url (the default ollama serving
// address) is accepted for the ollama provider even without
// MIVIA_ALLOW_INSECURE_HTTP, while the same loopback URL stays gated for
// other providers and a non-loopback http URL is never relaxed for ollama.
// The env var is pinned explicitly so the case never depends on the ambient
// environment (mirroring TestLoadHTTPBaseURLEnvGate's t.Setenv usage).
func TestValidateBaseURLOllamaLoopbackRelaxation(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "")
	tests := []struct {
		name            string
		raw             string
		provider        string
		wantErrContains string
	}{
		{"ollama http loopback relaxed without env", "http://127.0.0.1:11434/v1", "ollama", ""},
		{"non-ollama loopback not relaxed", "http://127.0.0.1:11434/v1", "deepseek", "https"},
		{"ollama https accepted", "https://ollama.com/v1", "ollama", ""},
		{"ollama non-loopback http rejected", "http://evil.example", "ollama", "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBaseURL(tt.raw, tt.provider)
			if tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("validateBaseURL(%q, %q) = %v, want nil", tt.raw, tt.provider, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("validateBaseURL(%q, %q) = %v, want error containing %q", tt.raw, tt.provider, err, tt.wantErrContains)
			}
		})
	}
}
