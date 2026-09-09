package uiadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestApplyGeneral_SyncStreamAssistant_Persists pins syncEditToSettings'
// SetSyncStreamAssistant case: this edit type must materialize
// [sync].stream_assistant into the config file, the same persistence path
// SetSyncIncludeThinking/SetSyncIncludeToolIO already exercise elsewhere,
// but for the one sync key none of those cover.
func TestApplyGeneral_SyncStreamAssistant_Persists(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")
	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncStreamAssistant{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(data), "stream_assistant") {
		t.Fatalf("persisted config = %s, want a [sync] stream_assistant key", data)
	}
}
