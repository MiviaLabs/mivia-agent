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

	_, _, err := s.mutateGeneral(ports.SetSyncIncludeThinking{On: false}, prior)
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

	_, _, err := s.mutateGeneral(ports.SetSyncIncludeToolIO{On: false}, prior)
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

	_, _, err := s.mutateGeneral(ports.SetSyncStreamAssistant{On: false}, prior)
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

// TestMutateGeneral_SyncVariantsReturnSyncNotifier pins the live re-arm
// seam: a SetSync* edit, when a syncOptsNotifier has been wired into
// the store, must return that notifier so applyGeneral can fire it
// after a successful persist. When no notifier is wired (the
// pre-launcher state used by every other general test), the returned
// syncNotifier is nil - applyGeneral's `if syncNotifier != nil` skips
// the fan-out, which is correct. The shape of the notifier itself is
// pinned by TestSettingsStore_ApplySync_FiresLiveReArmNotifier (an
// integration test that wires a recording closure); this test pins
// the mutateGeneral half.
//
// The three SetSync* variants all share the same notifier shape, so
// one assertion per variant is enough.
func TestMutateGeneral_SyncVariantsReturnSyncNotifier(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*SettingsStore, bool) (func(bool), func(bool, bool, bool), error)
	}{
		{
			name: "SetSyncIncludeThinking",
			apply: func(s *SettingsStore, on bool) (func(bool), func(bool, bool, bool), error) {
				return s.mutateGeneral(ports.SetSyncIncludeThinking{On: on}, s.snapshotGeneralPrior())
			},
		},
		{
			name: "SetSyncIncludeToolIO",
			apply: func(s *SettingsStore, on bool) (func(bool), func(bool, bool, bool), error) {
				return s.mutateGeneral(ports.SetSyncIncludeToolIO{On: on}, s.snapshotGeneralPrior())
			},
		},
		{
			name: "SetSyncStreamAssistant",
			apply: func(s *SettingsStore, on bool) (func(bool), func(bool, bool, bool), error) {
				return s.mutateGeneral(ports.SetSyncStreamAssistant{On: on}, s.snapshotGeneralPrior())
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Variant 1: no notifier wired - returns nil syncNotifier.
			// This is the default state of every other general test.
			sNoNotif := NewSettingsStore(nil, &config.Resolved{
				Sync: config.ResolvedSync{
					IncludeThinking: true,
					IncludeToolIO:   true,
					StreamAssistant: true,
				},
			}, nil)
			_, syncNoNotif, err := tc.apply(sNoNotif, false)
			if err != nil {
				t.Fatalf("mutateGeneral (no notifier wired): %v", err)
			}
			if syncNoNotif != nil {
				t.Errorf("mutateGeneral(%s) returned a non-nil sync notifier when no launcher is wired; would call into nil and panic. nil is the expected default", tc.name)
			}

			// Variant 2: notifier wired - returns that exact closure so
			// applyGeneral can fire it.
			sWithNotif := NewSettingsStore(nil, &config.Resolved{
				Sync: config.ResolvedSync{
					IncludeThinking: true,
					IncludeToolIO:   true,
					StreamAssistant: true,
				},
			}, nil)
			called := 0
			sWithNotif.SetSyncOptsNotifier(func(includeThinking, includeToolIO, streamAssistant bool) {
				called++
			})
			_, syncWithNotif, err := tc.apply(sWithNotif, false)
			if err != nil {
				t.Fatalf("mutateGeneral (notifier wired): %v", err)
			}
			if syncWithNotif == nil {
				t.Fatalf("mutateGeneral(%s) returned a nil sync notifier even though one was wired; applyGeneral will not fan-out the toggle to live sessions", tc.name)
			}
			// Drive it - this is exactly what applyGeneral does in its
			// `if syncNotifier != nil { go syncNotifier(...) }` block.
			syncWithNotif(false, false, false)
			if called != 1 {
				t.Errorf("synced notifier call count = %d, want 1", called)
			}
		})
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
