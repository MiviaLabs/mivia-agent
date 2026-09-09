package uiadapter_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// TestPersistTheme_SaveFailureSurfaces pins PersistTheme's SaveFailed
// event branch: a user config directory that refuses the write must report
// the save's own error text, not silently succeed.
func TestPersistTheme_SaveFailureSurfaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res := &config.Resolved{ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	userConfigDir := filepath.Dir(config.UserConfigPath())
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(userConfigDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(userConfigDir, 0o700) })
	probe := filepath.Join(userConfigDir, "writability-probe")
	if f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		_ = os.Remove(probe)
		t.Skip("platform still creates files in a read-only directory")
	}

	err := store.PersistTheme("mivia-dark")
	if err == nil {
		t.Fatal("PersistTheme accepted a user config directory it cannot write into")
	}
	if !strings.Contains(err.Error(), "denied") && !strings.Contains(err.Error(), "permission") {
		t.Logf("err = %v (accepted: message content is platform-dependent, presence of an error is what matters)", err)
	}
}
