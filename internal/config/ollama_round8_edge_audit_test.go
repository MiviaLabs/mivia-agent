package config

// Round-8 hostile audit of IsOllamaLoopback / validateBaseURL edge inputs.
// The predicate is hostname-literal per the locked plan (§3.1), so
// malformed-but-loopback URL forms (trailing space in path, empty fragment,
// empty/out-of-range port, unbracketed IPv6-with-port) are APPROVED. That is
// safe: the provider layer (newLoopbackDialContext) independently re-verifies
// the host and pins every dial, so keyless traffic can only ever reach a
// verified loopback address (see TestRound8PinnedDialCoversApprovedEdgeURLs).
// This file pins the predicate's actual contract and the fail-closed cases.

import (
	"os"
	"strings"
	"testing"

	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

func TestRound8IsOllamaLoopbackEdgeInputs(t *testing.T) {
	cases := []struct {
		raw   string
		want  bool
		label string
	}{
		// Loopback literals, any case, any scheme: approved (documented).
		{"http://127.0.0.1:11434/v1", true, "ipv4"},
		{"https://127.0.0.1:11434/v1", true, "ipv4-https"},
		{"http://[::1]:11434/v1", true, "ipv6-bracketed"},
		{"http://localhost:11434/v1", true, "localhost"},
		{"http://LOCALHOST:11434/v1", true, "localhost-upper"},
		{"http://LocalHost:11434/v1", true, "localhost-mixed"},
		// Trailing slash variants: hostname-based, so approved.
		{"http://127.0.0.1:11434/v1/", true, "trailing-slash"},
		{"http://127.0.0.1:11434/v1///", true, "many-slash"},

		// Malformed-but-loopback-host forms url.Parse accepts: approved.
		// Dial remains loopback-pinned, so no keyless traffic can escape.
		{"http://127.0.0.1:11434/v1 ", true, "trailing-space"},
		{"http://127.0.0.1:11434/v1#", true, "empty-fragment"},
		{"http://127.0.0.1:/v1", true, "empty-port"},
		{"http://127.0.0.1:99999/v1", true, "port-out-of-range"},
		{"http://::1:11434/v1", true, "unbracketed-ipv6-with-port"},

		// Fail-closed (the security-relevant cases).
		{"http://127.0.0.1.evil.com/v1", false, "dotted-suffix"},
		{"http://localhost.:11434/v1", false, "trailing-dot"},
		{"http://u@127.0.0.1:11434/v1", false, "userinfo"},
		{"http://127.0.0.1:11434/v1#frag", false, "fragment"},
		{"https://ollama.com/v1", false, "cloud"},
		{"http://10.0.0.1/v1", false, "private-not-loopback"},
		{"http://127.0.0.2/v1", false, "loopback-range-not-literal"},
		{"ftp://127.0.0.1/v1", false, "wrong-scheme"},
		{"127.0.0.1:11434", false, "no-scheme"},
		{"", false, "empty"},
		{"http:///v1", false, "no-host"},
		{"http://[::1/v1", false, "bad-ipv6"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := IsOllamaLoopback(tc.raw); got != tc.want {
				t.Errorf("IsOllamaLoopback(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRound8ValidateBaseURLLoopbackRelaxationIsProviderAgnostic pins
// validateBaseURL's OWN contract only: the https requirement relaxes for
// ANY provider name on a verified loopback base_url, not just "ollama" -
// every builtin provider now gets the same dial-pinning protection at
// construction (provider.NewForProvider), so this check no longer has a
// reason to single one out. This function was never about keyless-ness
// (that is a separate, still ollama-only gate in provider.NewForProvider);
// the prior name overstated its scope.
func TestRound8ValidateBaseURLLoopbackRelaxationIsProviderAgnostic(t *testing.T) {
	cases := []struct {
		raw        string
		name       string
		want       error
		wantSubstr string // when want != nil: required error substring ("" = any error)
	}{
		{"http://127.0.0.1:11434/v1", "ollama", nil, ""},
		{"https://127.0.0.1:11434/v1", "ollama", nil, ""},
		{"https://ollama.com/v1", "ollama", nil, ""},
		{"http://ollama.example.com/v1", "ollama", errInsecureHTTP, "https"},
		{"http://127.0.0.1.evil.com/v1", "ollama", errInsecureHTTP, "https"},
		{"http://localhost./v1", "ollama", errInsecureHTTP, "https"},
		// Userinfo is rejected earlier by URL validation ("base_url is
		// invalid") - still fail-closed, just a different message.
		{"http://u@127.0.0.1:11434/v1", "ollama", errInsecureHTTP, "invalid"},
		{"http://127.0.0.1:11434/v1", "deepseek", nil, ""},
		{"http://127.0.0.1:11434/v1", "openrouter", nil, ""},
		{"http://127.0.0.1:11434/v1", "zai", nil, ""},
		{"http://127.0.0.1:11434/v1", "OLLAMA", nil, ""}, // case is normalized before this point
		// A non-loopback host still needs https regardless of provider name.
		{"http://example.com:11434/v1", "deepseek", errInsecureHTTP, "https"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_"+tc.raw, func(t *testing.T) {
			err := validateBaseURL(tc.raw, tc.name)
			if tc.want == nil {
				if err != nil {
					t.Errorf("validateBaseURL(%q, %q) = %v, want nil", tc.raw, tc.name, err)
				}
				return
			}
			if err == nil {
				t.Errorf("validateBaseURL(%q, %q) = nil, want rejection", tc.raw, tc.name)
				return
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("validateBaseURL(%q, %q) = %v, want error containing %q", tc.raw, tc.name, err, tc.wantSubstr)
			}
		})
	}
}

var errInsecureHTTP = &baseURLError{}

type baseURLError struct{}

func (e *baseURLError) Error() string { return "base_url must use https" }

// TestRound8ExampleConfigsLoad verifies the shipped example config behaves as
// documented: the ollama cloud profile (key required) and the local-daemon
// profile (no key) both load, and .env.example loads with all keys unset.
func TestRound8ExampleConfigsLoad(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ZAI_API_KEY", "")
	root := repoRoot(t)
	// .env.example must parse and leave every key unset (blank values).
	if _, err := sdkenvfile.Load(root + "/.env.example"); err != nil {
		t.Fatalf(".env.example load: %v", err)
	}
	exampleCfg := root + "/.mivia/mivia.toml.example"
	res, err := Load(LoadOptions{ConfigPath: exampleCfg})
	if err != nil {
		t.Fatalf("load shipped example config: %v", err)
	}
	// Active provider is deepseek; the ollama cloud profile must be present
	// and key-required.
	ollama, ok := res.ProviderRuntimes["ollama"]
	if !ok {
		t.Fatal("shipped example config has no [providers.ollama] runtime")
	}
	if ollama.BaseURL != "https://ollama.com/v1" {
		t.Fatalf("ollama cloud base_url = %q, want https://ollama.com/v1", ollama.BaseURL)
	}
	if IsOllamaLoopback(ollama.BaseURL) {
		t.Fatal("ollama cloud base_url must NOT be loopback")
	}
	if ollama.APIKeySet {
		t.Fatal("ollama cloud runtime unexpectedly keyed with env cleared")
	}

	// Local-daemon variant: same profile with the loopback base_url must be
	// keyless (mode inferred from base_url alone, per the example comments).
	dir := t.TempDir()
	localCfg := dir + "/mivia.toml"
	data, err := os.ReadFile(exampleCfg)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(data), `base_url = "https://ollama.com/v1"`, `base_url = "http://127.0.0.1:11434/v1"`, 1)
	if err := os.WriteFile(localCfg, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := Load(LoadOptions{ConfigPath: localCfg})
	if err != nil {
		t.Fatalf("load local-daemon variant: %v", err)
	}
	lollama, ok := local.ProviderRuntimes["ollama"]
	if !ok {
		t.Fatal("local variant lost [providers.ollama]")
	}
	if !IsOllamaLoopback(lollama.BaseURL) {
		t.Fatalf("local base_url %q must be loopback", lollama.BaseURL)
	}
	if lollama.APIKeySet {
		t.Fatal("local-daemon profile must not require a key")
	}
	if lollama.APIKeyEnv != "OLLAMA_API_KEY" {
		t.Fatalf("api_key_env = %q, want OLLAMA_API_KEY", lollama.APIKeyEnv)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Test CWD is the package dir (internal/config). Climb to the repo root.
	for d := wd; d != "/" && d != ""; d = d[:strings.LastIndex(d, "/")] {
		if _, err := os.Stat(d + "/.mivia/mivia.toml.example"); err == nil {
			return d
		}
	}
	t.Fatal("cannot locate repo root")
	return ""
}
