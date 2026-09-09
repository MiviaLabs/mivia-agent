package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func memoryTestResolved(enabled bool) *config.Resolved {
	return &config.Resolved{ProviderName: "deepseek", Model: "deepseek-v4-flash",
		Memory: config.MemoryConfig{Enabled: &enabled, StoreBackend: "markdown"}}
}

func TestConfigureChatWorkspaceWiresMemoryTools(t *testing.T) {
	res := memoryTestResolved(true)
	sess := chat.NewSession(res, nil)
	closeStore, err := ConfigureChatWorkspace(sess, t.TempDir(), true, res, nil, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	for _, name := range []string{"memory_save", "memory_search"} {
		if _, ok := sess.Tools.Get(name); !ok {
			t.Errorf("%s not registered", name)
		}
	}
}

func TestConfigureChatWorkspaceOmitsDisabledMemoryTools(t *testing.T) {
	res := memoryTestResolved(false)
	sess := chat.NewSession(res, nil)
	closeStore, err := ConfigureChatWorkspace(sess, t.TempDir(), true, res, nil, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if _, ok := sess.Tools.Get("memory_save"); ok {
		t.Fatal("memory_save registered")
	}
}
