package config

import (
	"strings"
	"testing"
)

// TestResolveProviderRuntimesOllamaLoopback pins the local-Ollama credential
// relaxation: an ollama provider on a loopback endpoint needs no API key to
// be selectable; an empty model list still disables the group; keyed
// providers keep their existing behavior.
func TestResolveProviderRuntimesOllamaLoopback(t *testing.T) {
	oneModel := []ModelSpec{{Name: "llama3.2", ContextWindowTokens: 128000}}
	prov := func(name, baseURL string, models []ModelSpec) map[string]ProviderConfig {
		return map[string]ProviderConfig{name: {APIKeyEnv: strings.ToUpper(name) + "_API_KEY", BaseURL: baseURL, Models: models}}
	}

	tests := []struct {
		name      string
		providers map[string]ProviderConfig
		envMap    map[string]string
		wantName  string
		wantSel   bool
		wantWhy   string
	}{
		{
			name:      "ollama loopback without key is selectable",
			providers: prov("ollama", "http://127.0.0.1:11434/v1", oneModel),
			envMap:    map[string]string{},
			wantName:  "ollama",
			wantSel:   true,
			wantWhy:   "",
		},
		{
			name:      "ollama loopback without models stays disabled",
			providers: prov("ollama", "http://127.0.0.1:11434/v1", nil),
			envMap:    map[string]string{},
			wantName:  "ollama",
			wantSel:   false,
			wantWhy:   "no configured models",
		},
		{
			name:      "keyed provider stays selectable",
			providers: prov("deepseek", "https://api.deepseek.com", oneModel),
			envMap:    map[string]string{"DEEPSEEK_API_KEY": "sk-test"},
			wantName:  "deepseek",
			wantSel:   true,
			wantWhy:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, groups := resolveProviderRuntimes(File{Providers: tt.providers}, tt.envMap, tt.wantName)
			group := findProviderGroup(groups, tt.wantName)
			if group == nil {
				t.Fatalf("no group for provider %q in %d groups", tt.wantName, len(groups))
			}
			if group.Selectable != tt.wantSel {
				t.Fatalf("group %q Selectable = %v, want %v", tt.wantName, group.Selectable, tt.wantSel)
			}
			if group.DisabledReason != tt.wantWhy {
				t.Fatalf("group %q DisabledReason = %q, want %q", tt.wantName, group.DisabledReason, tt.wantWhy)
			}
		})
	}
}

// TestResolveProviderRuntimesLoopbackRelaxationIsNarrow pins that the
// relaxation is provider-specific, not URL-specific: a non-ollama provider
// on a loopback URL, and a non-loopback ollama URL, both keep the
// key-required behavior.
func TestResolveProviderRuntimesLoopbackRelaxationIsNarrow(t *testing.T) {
	oneModel := []ModelSpec{{Name: "llama3.2", ContextWindowTokens: 128000}}
	prov := func(name, baseURL string, models []ModelSpec) map[string]ProviderConfig {
		return map[string]ProviderConfig{name: {APIKeyEnv: strings.ToUpper(name) + "_API_KEY", BaseURL: baseURL, Models: models}}
	}

	tests := []struct {
		name      string
		providers map[string]ProviderConfig
		envMap    map[string]string
		wantName  string
		wantWhy   string
	}{
		{
			name:      "cloud ollama without key is not selectable",
			providers: prov("ollama", "https://ollama.com/v1", oneModel),
			envMap:    map[string]string{},
			wantName:  "ollama",
			wantWhy:   "credential unavailable",
		},
		{
			name:      "non-ollama provider on loopback URL is not relaxed",
			providers: prov("deepseek", "http://127.0.0.1:9999", oneModel),
			envMap:    map[string]string{},
			wantName:  "deepseek",
			wantWhy:   "credential unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, groups := resolveProviderRuntimes(File{Providers: tt.providers}, tt.envMap, tt.wantName)
			group := findProviderGroup(groups, tt.wantName)
			if group == nil {
				t.Fatalf("no group for provider %q in %d groups", tt.wantName, len(groups))
			}
			if group.Selectable {
				t.Fatalf("group %q Selectable = true, want false", tt.wantName)
			}
			if group.DisabledReason != tt.wantWhy {
				t.Fatalf("group %q DisabledReason = %q, want %q", tt.wantName, group.DisabledReason, tt.wantWhy)
			}
		})
	}
}

func findProviderGroup(groups []ProviderModelGroup, name string) *ProviderModelGroup {
	for i := range groups {
		if groups[i].Provider == name {
			return &groups[i]
		}
	}
	return nil
}
