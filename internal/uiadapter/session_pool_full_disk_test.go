package uiadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func liveAccessPool(t *testing.T) (*SessionPool, *SettingsStore, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubWorkflowWiring(t)
	root := t.TempDir()
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: root}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "launch"
	closeFn, err := cliagents.ConfigureChatWorkspace(sess, root, true, res, state, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	pool := NewSessionPool(sess, res, state, true)
	t.Cleanup(func() { pool.CloseAll() })
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	return pool, NewSettingsStore(sess, res, state), outside
}

func setPoolAccess(t *testing.T, store *SettingsStore, on bool) {
	t.Helper()
	handle, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetFullDiskAccess{On: on})
	if err != nil {
		t.Fatal(err)
	}
	var last ports.SaveEvent
	for event := range handle.Events() {
		last = event
	}
	if last.State != ports.SaveSaved {
		t.Fatalf("settings save = %v: %s", last.State, last.Message)
	}
}

func assertPoolAccess(t *testing.T, reg *tools.Registry, path string, on bool) {
	t.Helper()
	args, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.Execute(context.Background(), "read_file", args)
	if (err == nil) != on {
		t.Errorf("outside read: err=%v, want allowed=%v", err, on)
	}
	if got := reg.WorkspaceUnrestricted(); got != on {
		t.Errorf("registry access = %v, want %v", got, on)
	}
}

func createAccessSession(t *testing.T, pool *SessionPool, root string) *chat.Session {
	t.Helper()
	conv, err := pool.CreateFreshInDir(nil, root)
	if err != nil {
		t.Fatal(err)
	}
	return conv.(*Conversation).Session()
}

func TestPoolSettingsLiveAccessCacheAndNewRoots(t *testing.T) {
	pool, store, outside := liveAccessPool(t)
	root := t.TempDir()
	first := createAccessSession(t, pool, root)
	for _, on := range []bool{true, false} {
		setPoolAccess(t, store, on)
		cached := createAccessSession(t, pool, root)
		if cached.Tools != first.Tools {
			t.Fatal("cached root rebuilt its registry")
		}
		fresh := createAccessSession(t, pool, t.TempDir())
		for _, sess := range []*chat.Session{pool.Session("launch"), first, cached, fresh} {
			assertPoolAccess(t, sess.Tools, outside, on)
			if sess.ToolBaseResolver != nil {
				assertPoolAccess(t, sess.ToolBaseResolver(), outside, on)
			}
		}
	}
}

func TestPoolSettingsLiveAccessDuringBuild(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(map[bool]string{false: "enable", true: "disable"}[initial], func(t *testing.T) {
			pool, store, outside := liveAccessPool(t)
			setPoolAccess(t, store, initial)
			entered, release := make(chan struct{}), make(chan struct{})
			cliagents.WireWorkflowToolOptionsVar = func(*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, bool, ledger.LedgerRepository) {
				close(entered)
				<-release
			}
			result := make(chan error, 1)
			root := t.TempDir()
			go func() {
				_, err := pool.CreateFreshInDir(nil, root)
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("builder did not reach workflow wiring")
			}
			setPoolAccess(t, store, !initial)
			close(release)
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("builder did not finish")
			}
			sess := pool.lastCreated.Session()
			assertPoolAccess(t, sess.Tools, outside, !initial)
			assertPoolAccess(t, sess.ToolBaseResolver(), outside, !initial)
		})
	}
}
