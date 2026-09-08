package uiadapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestBuildGeneralView_SyncSeedsFromResolved(t *testing.T) {
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			IncludeThinking: false,
			IncludeToolIO:   true,
			StreamAssistant: false,
		},
	}
	s := NewSettingsStore(nil, res, nil)
	v := s.buildGeneralView("")

	if v.SyncIncludeThinking != false {
		t.Errorf("SyncIncludeThinking = %v, want false", v.SyncIncludeThinking)
	}
	if v.SyncIncludeToolIO != true {
		t.Errorf("SyncIncludeToolIO = %v, want true", v.SyncIncludeToolIO)
	}
	if v.SyncStreamAssistant != false {
		t.Errorf("SyncStreamAssistant = %v, want false", v.SyncStreamAssistant)
	}
}

func TestBuildGeneralView_SyncDefaultsTrueWhenResolvedNil(t *testing.T) {
	s := NewSettingsStore(nil, nil, nil)
	v := s.buildGeneralView("")

	if v.SyncIncludeThinking != true {
		t.Errorf("SyncIncludeThinking = %v, want true", v.SyncIncludeThinking)
	}
	if v.SyncIncludeToolIO != true {
		t.Errorf("SyncIncludeToolIO = %v, want true", v.SyncIncludeToolIO)
	}
	if v.SyncStreamAssistant != true {
		t.Errorf("SyncStreamAssistant = %v, want true", v.SyncStreamAssistant)
	}
}

func TestMutateGeneral_SetSyncIncludeThinking_UpdatesViewOnly(t *testing.T) {
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			IncludeThinking: true,
		},
	}
	s := NewSettingsStore(nil, res, nil)
	prior := s.snapshotGeneralPrior()

	_, err := s.mutateGeneral(ports.SetSyncIncludeThinking{On: false}, prior)
	if err != nil {
		t.Fatalf("mutateGeneral failed: %v", err)
	}

	if s.general.SyncIncludeThinking != false {
		t.Errorf("s.general.SyncIncludeThinking = %v, want false", s.general.SyncIncludeThinking)
	}
	if s.res.Sync.IncludeThinking != true {
		t.Errorf("s.res.Sync.IncludeThinking changed to %v, want true (no mirror invariant)", s.res.Sync.IncludeThinking)
	}
}

func TestMutateGeneral_SetSyncIncludeToolIO_UpdatesViewOnly(t *testing.T) {
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			IncludeToolIO: true,
		},
	}
	s := NewSettingsStore(nil, res, nil)
	prior := s.snapshotGeneralPrior()

	_, err := s.mutateGeneral(ports.SetSyncIncludeToolIO{On: false}, prior)
	if err != nil {
		t.Fatalf("mutateGeneral failed: %v", err)
	}

	if s.general.SyncIncludeToolIO != false {
		t.Errorf("s.general.SyncIncludeToolIO = %v, want false", s.general.SyncIncludeToolIO)
	}
	if s.res.Sync.IncludeToolIO != true {
		t.Errorf("s.res.Sync.IncludeToolIO changed to %v, want true (no mirror invariant)", s.res.Sync.IncludeToolIO)
	}
}

func TestMutateGeneral_SetSyncStreamAssistant_UpdatesViewOnly(t *testing.T) {
	res := &config.Resolved{
		Sync: config.ResolvedSync{
			StreamAssistant: true,
		},
	}
	s := NewSettingsStore(nil, res, nil)
	prior := s.snapshotGeneralPrior()

	_, err := s.mutateGeneral(ports.SetSyncStreamAssistant{On: false}, prior)
	if err != nil {
		t.Fatalf("mutateGeneral failed: %v", err)
	}

	if s.general.SyncStreamAssistant != false {
		t.Errorf("s.general.SyncStreamAssistant = %v, want false", s.general.SyncStreamAssistant)
	}
	if s.res.Sync.StreamAssistant != true {
		t.Errorf("s.res.Sync.StreamAssistant changed to %v, want true (no mirror invariant)", s.res.Sync.StreamAssistant)
	}
}

func TestGeneralViewToSettings_LeavesSyncPointersNil(t *testing.T) {
	v := ports.GeneralView{
		SyncIncludeThinking: true,
		SyncIncludeToolIO:   true,
		SyncStreamAssistant: true,
	}
	gs := generalViewToSettings(v)

	if gs.SyncIncludeThinking != nil {
		t.Errorf("gs.SyncIncludeThinking = %v, want nil", *gs.SyncIncludeThinking)
	}
	if gs.SyncIncludeToolIO != nil {
		t.Errorf("gs.SyncIncludeToolIO = %v, want nil", *gs.SyncIncludeToolIO)
	}
	if gs.SyncStreamAssistant != nil {
		t.Errorf("gs.SyncStreamAssistant = %v, want nil", *gs.SyncStreamAssistant)
	}
}

func TestApplyGeneral_PersistsOnlyTheTouchedSyncKey(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	initialTOML := `[sync]
enabled = true
api_url = "https://api.example.com"
`
	if err := os.WriteFile(cfgPath, []byte(initialTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{
		ConfigPath: cfgPath,
		Sync: config.ResolvedSync{
			IncludeThinking: true,
			IncludeToolIO:   true,
			StreamAssistant: true,
		},
	}
	s := NewSettingsStore(nil, res, nil)

	s.mu.Lock()
	err := s.applyGeneral(ports.SetSyncIncludeThinking{On: false})
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("applyGeneral failed: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] table in %s", string(data))
	}

	val, hasThinking := syncMap["include_thinking"]
	if !hasThinking || val != false {
		t.Errorf("include_thinking = %v (present=%v), want false", val, hasThinking)
	}
	if _, hasToolIO := syncMap["include_tool_io"]; hasToolIO {
		t.Errorf("include_tool_io unexpectedly present in [sync] table")
	}
	if _, hasStream := syncMap["stream_assistant"]; hasStream {
		t.Errorf("stream_assistant unexpectedly present in [sync] table")
	}
}
