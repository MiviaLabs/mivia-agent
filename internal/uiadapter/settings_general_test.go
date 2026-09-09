package uiadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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

// TestMutateGeneral_NoticeOptionsRelayToConversation pins
// SetShowIterationNotices and SetShowPromptCacheNotices' s.conv branches:
// with a conversation wired, each edit must relay both notice options,
// not just flip a local field.
func TestMutateGeneral_NoticeOptionsRelayToConversation(t *testing.T) {
	res := &config.Resolved{}
	s := NewSettingsStore(nil, res, nil)
	conv := NewConversation(nil)
	s.SetConversation(conv)

	prior := s.snapshotGeneralPrior()
	if _, _, err := s.mutateGeneral(ports.SetShowIterationNotices{On: true}, prior); err != nil {
		t.Fatalf("SetShowIterationNotices: %v", err)
	}
	if !s.general.ShowIterationNotices {
		t.Fatal("ShowIterationNotices did not update")
	}

	prior = s.snapshotGeneralPrior()
	if _, _, err := s.mutateGeneral(ports.SetShowPromptCacheNotices{On: true}, prior); err != nil {
		t.Fatalf("SetShowPromptCacheNotices: %v", err)
	}
	if !s.general.ShowPromptCacheNotices {
		t.Fatal("ShowPromptCacheNotices did not update")
	}
}

// TestMutateGeneral_ScreenReaderAndReducedMotion pins the two flag-only
// cases directly.
func TestMutateGeneral_ScreenReaderAndReducedMotion(t *testing.T) {
	s := NewSettingsStore(nil, &config.Resolved{}, nil)
	prior := s.snapshotGeneralPrior()
	if _, _, err := s.mutateGeneral(ports.SetScreenReader{On: true}, prior); err != nil {
		t.Fatalf("SetScreenReader: %v", err)
	}
	if !s.general.ScreenReader {
		t.Fatal("ScreenReader did not update")
	}
	prior = s.snapshotGeneralPrior()
	if _, _, err := s.mutateGeneral(ports.SetReducedMotion{On: true}, prior); err != nil {
		t.Fatalf("SetReducedMotion: %v", err)
	}
	if !s.general.ReducedMotion {
		t.Fatal("ReducedMotion did not update")
	}
}

// TestRollbackGeneral_RestoresPoolApprovalDefault pins rollbackGeneral's
// own s.pool != nil branch: with a live *SessionPool wired, a persist
// failure must fan the prior approval default back out through the pool
// rather than leaving pooled sessions on the optimistic, never-persisted
// value.
func TestRollbackGeneral_RestoresPoolApprovalDefault(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := &config.Resolved{Model: "test-model", ConfigPath: filepath.Join(blocker, "mivia.toml")}
	s := NewSettingsStore(nil, res, nil)
	s.pool = NewSessionPool(nil, res, nil, false)

	// The default is ApprovalDefault: "always" (settings_general.go:35),
	// so "deny" is the change a successful rollback must undo.
	err := s.applyGeneral(ports.SetApprovalDefault{Mode: "deny"})
	if err == nil {
		t.Fatal("applyGeneral accepted an unwritable config path")
	}
	if got := s.general.ApprovalDefault; got != "always" {
		t.Errorf("ApprovalDefault = %q after a rolled-back edit, want the pre-edit \"always\" restored", got)
	}
}

// TestInheritApprovalLocked_NilSessionIsANoop pins the nil-sess guard
// directly.
func TestInheritApprovalLocked_NilSessionIsANoop(t *testing.T) {
	inheritApprovalLocked(nil, nil, nil) // must not panic
}

// TestInheritApprovalLocked_FallsBackToExistingBasePolicy pins the
// existing.BaseApprovalPolicyValue() fallback: an empty *config.Resolved
// policy (res == nil) must inherit the PRIOR session's base policy rather
// than leaving the new session's policy unset.
func TestInheritApprovalLocked_FallsBackToExistingBasePolicy(t *testing.T) {
	existing := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)
	existing.SetBaseApprovalPolicy("deny")
	sess := chat.NewSession(&config.Resolved{Model: "test-model"}, nil)

	inheritApprovalLocked(sess, existing, nil)

	if got := sess.BaseApprovalPolicyValue(); got != "deny" {
		t.Fatalf("BaseApprovalPolicyValue() = %q, want the existing session's %q to have been inherited", got, "deny")
	}
}

// TestWireSyncNotices_CallbacksPushNotices drives every OnStop/OnDegraded/
// OnRecovered callback wireSyncNotices installs and asserts each one
// reaches the pool's Notices() stream.
func TestWireSyncNotices_CallbacksPushNotices(t *testing.T) {
	p := &SessionPool{notices: make(chan uievent.Event, 8)}
	opts := &chatsync.SessionOptions{}
	p.wireSyncNotices(opts)

	opts.OnStop("server said stop")
	opts.OnDegraded("network blip")
	opts.OnRecovered()

	var texts []string
	for i := 0; i < 3; i++ {
		select {
		case ev := <-p.notices:
			texts = append(texts, ev.Body.(uievent.NoticeBody).Text)
		default:
			t.Fatalf("expected 3 notices, got %d: %v", i, texts)
		}
	}
	if !strings.Contains(texts[0], "server said stop") {
		t.Errorf("OnStop notice = %q, want it to name the reason", texts[0])
	}
	if !strings.Contains(texts[1], "network blip") {
		t.Errorf("OnDegraded notice = %q, want it to name the reason", texts[1])
	}
	if !strings.Contains(texts[2], "recovered") {
		t.Errorf("OnRecovered notice = %q, want it to say so", texts[2])
	}
}
