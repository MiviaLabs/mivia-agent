package provider

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestFactoryRegistryRejectsDuplicateAndKeepsSortedNames(t *testing.T) {
	r := newFactoryRegistry()
	factory := func(Options) (Completer, error) { return nil, nil }
	if err := r.register("openrouter", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.register("deepseek", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.register("DeepSeek", factory); err == nil {
		t.Fatal("expected duplicate error")
	}
	if got := strings.Join(r.names(), ","); got != "deepseek,openrouter" {
		t.Fatalf("names=%q", got)
	}
}

func TestNewDispatchesBuiltinsAndRejectsUnknown(t *testing.T) {
	res := &config.Resolved{ProviderName: "deepseek", BaseURL: "https://example.com/v1", APIKey: "fake", APIKeySet: true}
	comp, err := New(res)
	if err != nil || comp.Name() != "deepseek" {
		t.Fatalf("comp=%T err=%v", comp, err)
	}
	res.ProviderName = "unknown"
	_, err = New(res)
	if err == nil || !strings.Contains(err.Error(), "available: deepseek, openrouter") {
		t.Fatalf("err=%v", err)
	}
}
