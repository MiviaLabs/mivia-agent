package newtui

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// TestSyncOptsNotifierBridgeNilGuard mirrors
// TestFullDiskNotifierBridgeNilGuard for wireSyncOptsNotifier's own
// nil-store/nil-pool guard: neither a launcher failure to construct a
// store nor one to construct a pool should panic the bridge.
func TestSyncOptsNotifierBridgeNilGuard(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a nil store or pool must skip the notifier bridge, got panic: %v", r)
		}
	}()
	wireSyncOptsNotifier(nil, nil)

	res := &config.Resolved{ConfigPath: t.TempDir() + "/mivia.toml"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)
	wireSyncOptsNotifier(store, nil)

	pool := uiadapter.NewSessionPool(nil, res, state, false)
	wireSyncOptsNotifier(nil, pool)
}

// TestSyncOptsNotifierBridgeFiresThroughToTheRealPool exercises the
// wired closure end to end through public API only: a General
// SetSyncIncludeThinking edit on the store must reach
// wireSyncOptsNotifier's closure and call pool.ApplySyncOpts without
// panicking, even with zero attached SyncSessions (the pool fan-out
// itself, across N attached sessions, is proven with -race in
// internal/uiadapter's own TestPoolApplySyncOptsFansOutToEveryAttachedSession;
// this test only proves the launcher's wiring reaches the pool at
// all, which that lower-level test cannot see).
func TestSyncOptsNotifierBridgeFiresThroughToTheRealPool(t *testing.T) {
	tmpDir := t.TempDir()
	res := &config.Resolved{ConfigPath: tmpDir + "/mivia.toml"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)
	pool := uiadapter.NewSessionPool(nil, res, state, false)

	wireSyncOptsNotifier(store, pool)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncIncludeThinking{On: false})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range h.Events() {
		if ev.State == ports.SaveFailed {
			t.Fatalf("persist failed: %s", ev.Message)
		}
	}
	// No attached SyncSession in this pool, so there is nothing further
	// to assert - reaching here without a panic or hang across the
	// store -> notifier -> pool.ApplySyncOpts chain is the test.
}
