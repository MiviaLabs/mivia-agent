package provider

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestNewForProviderUsesQualifiedRuntimeRecord(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", Model: "deepseek/v4", ProviderRuntimes: map[string]config.ProviderRuntime{
		"deepseek": {ProviderName: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKeyEnv: "DEEPSEEK_API_KEY", APIKeySet: true, APIKey: "key"},
		"zai":      {ProviderName: "zai", BaseURL: "https://api.z.ai/api/paas/v4", APIKeyEnv: "ZAI_API_KEY", APIKeySet: true, APIKey: "key"},
	}}
	for _, name := range []string{"deepseek", "zai"} {
		comp, err := NewForProvider(res, name)
		if err != nil || comp == nil {
			t.Fatalf("provider %s: comp=%v err=%v", name, comp, err)
		}
	}
}

func TestNewForProviderFailsClosedWithoutCredential(t *testing.T) {
	res := &config.Resolved{ProviderRuntimes: map[string]config.ProviderRuntime{
		"openrouter": {ProviderName: "openrouter", APIKeyEnv: "OPENROUTER_API_KEY"},
	}}
	_, err := NewForProvider(res, "openrouter")
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error exposed credential environment detail: %q", err)
	}
}
