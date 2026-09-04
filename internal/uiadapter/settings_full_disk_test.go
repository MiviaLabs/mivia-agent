package uiadapter_test

// The full-disk toggle's provenance pins (audit F1/F2): the TUI setting
// persists to the operator's USER config and ONLY there, never to the
// workspace's own committable .mivia/mivia.toml, no matter which file
// res.ConfigPath points at. General settings' other keys ride the generic
// full-view write to configPath (existing, tested behavior); this grant
// deliberately does not.

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

// fullDiskFixture builds a store whose res.ConfigPath IS the workspace's
// own .mivia/mivia.toml - the common launch shape (config candidates search
// cwd before the user path) and exactly the shape under which a naive
// general-setting would write into a repo-controlled file.
func fullDiskFixture(t *testing.T) (store *uiadapter.SettingsStore, wsConfig, userConfig string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	wsConfig = filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsConfig, []byte("[tui]\ntheme = \"mivia-dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{
		ConfigPath:   wsConfig,
		ProviderName: "ollama",
		Model:        "llama3.3",
	}
	state := &cliagents.AgentSessionState{
		Registry:      agents.NewRegistry(),
		WorkspaceRoot: ws,
	}
	userConfig = config.UserConfigPath()
	return uiadapter.NewSettingsStore(nil, res, state), wsConfig, userConfig
}

func drainFullDiskSave(t *testing.T, handle ports.SaveHandle) ports.SaveEvent {
	t.Helper()
	var last ports.SaveEvent
	for event := range handle.Events() {
		last = event
	}
	return last
}

func TestSettingsFullDiskAccessPersistsToUserConfigOnly(t *testing.T) {
	store, wsConfig, userConfig := fullDiskFixture(t)

	handle, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetFullDiskAccess{On: true})
	if err != nil {
		t.Fatal(err)
	}
	if last := drainFullDiskSave(t, handle); last.State != ports.SaveSaved {
		t.Fatalf("save state = %v (%s), want SaveSaved", last.State, last.Message)
	}

	if got := store.Settings().General.General().FullDiskAccess; !got {
		t.Fatal("view did not record the grant")
	}
	raw, err := os.ReadFile(wsConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "full_disk") || strings.Contains(string(raw), "workspace_access") {
		t.Fatalf("the grant leaked into the workspace's committable config:\n%s", raw)
	}
	userRaw, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userRaw), "full_disk = true") {
		t.Fatalf("user config missing the grant:\n%s", userRaw)
	}
	if !config.UserFullDiskAccessForWorkspace(filepath.Dir(filepath.Dir(wsConfig))) {
		t.Fatal("UserFullDiskAccessForWorkspace does not see the persisted grant")
	}
}

// TestSettingsFullDiskAccessRefusesSameFile pins the write guard at the
// store layer: when the user config resolves to the workspace's own config
// (workspace rooted at $HOME), the toggle must fail the save - never write
// a repo-distributed grant - and leave the file untouched.
func TestSettingsFullDiskAccessRefusesSameFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(homeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "[tui]\ntheme = \"mivia-dark\"\n"
	if err := os.WriteFile(homeConfig, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ConfigPath: homeConfig, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: home}
	store := uiadapter.NewSettingsStore(nil, res, state)

	handle, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetFullDiskAccess{On: true})
	if err != nil {
		t.Fatal(err)
	}
	if last := drainFullDiskSave(t, handle); last.State != ports.SaveFailed {
		t.Fatalf("save state = %v, want SaveFailed for a workspace-owned target", last.State)
	}
	raw, err := os.ReadFile(homeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("refused save still modified the file:\n%s", raw)
	}
	if store.Settings().General.General().FullDiskAccess {
		t.Fatal("view recorded a grant whose save failed")
	}
}

// TestSettingsGeneralViewSeedsFullDiskFromUserConfig pins the view's read
// provenance: the row reflects the operator's user config, and a workspace
// config claiming the key does NOT turn it on (the F1 clone attack, at the
// view layer).
func TestSettingsGeneralViewSeedsFullDiskFromUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfig := filepath.Join(home, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(userConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{ConfigPath: userConfig, ProviderName: "ollama", Model: "llama3.3"}
	ws := t.TempDir()
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: ws}
	store := uiadapter.NewSettingsStore(nil, res, state)
	if !store.Settings().General.General().FullDiskAccess {
		t.Fatal("view did not seed the grant from the operator user config")
	}

	// Now the clone attack: user config WITHOUT the key, workspace config
	// WITH it. The view must stay off.
	if err := os.WriteFile(userConfig, []byte("[tui]\ntheme = \"mivia-dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wsConfig := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(wsConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsConfig, []byte("[workspace_access]\nfull_disk = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res2 := &config.Resolved{ConfigPath: wsConfig, ProviderName: "ollama", Model: "llama3.3"}
	store2 := uiadapter.NewSettingsStore(nil, res2, state)
	if store2.Settings().General.General().FullDiskAccess {
		t.Fatal("view lifted confinement from a workspace-owned config; a cloned repo could self-grant")
	}
}
