package uiadapter_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// boundResumeFixture seeds a turn-only worktree session, simulates a restart,
// and returns a runner/pool over a fresh unbound session - the state a remote
// client (or the UI's background mount) is in when it reaches a worktree
// session by bare id, with no listing row to carry the instance.
type boundResumeFixture struct {
	runner   *uiadapter.CommandRunner
	pool     *uiadapter.SessionPool
	store    *storage.SQLite
	mainDir  string
	seedID   string
	instance contextstate.WorktreeInstance
}

func newBoundResumeFixture(t *testing.T) boundResumeFixture {
	t.Helper()
	return newBoundResumeFixtureWithTools(t, true)
}

// newBoundResumeFixtureWithTools builds the fixture with or without a tool
// registry on its pooled session: `mivia chat --no-tools` leaves every member
// with nil Tools while the context store is installed exactly the same.
func newBoundResumeFixtureWithTools(t *testing.T, toolsOn bool) boundResumeFixture {
	t.Helper()
	fx := worktreeCatalogFixtureNoClose(t)
	gitInitTempRepo(t, fx.MainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	seedID := seedTurnOnlyWorktreeSession(t, fx.Store, fx.MainDir, fx.WorktreeDir, res)
	fx.Store.Close()

	store, err := storage.OpenSQLite(fx.DBPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	restart := chat.NewSession(res, &nullCompleter{})
	restart.SessionID = "session-main-restart"
	if toolsOn {
		withTools(restart)
	}
	installCtx(t, restart, store, fx.MainDir)
	pool := uiadapter.NewSessionPool(restart, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(restart, pool, res, nil)
	t.Chdir(fx.MainDir)
	return boundResumeFixture{
		runner: runner, pool: pool, store: store, mainDir: fx.MainDir, seedID: seedID,
		instance: contextstate.WorktreeInstance{Worktree: "wt1", ID: resumeFixtureInstanceID},
	}
}

func assertRestoredWorktreeHistory(t *testing.T, conv ports.Conversation) {
	t.Helper()
	var hist []string
	for _, m := range conv.History() {
		hist = append(hist, m.Text)
	}
	joined := strings.Join(hist, "\n")
	for _, want := range []string{"wt turn one", "wt answer one"} {
		if !strings.Contains(joined, want) {
			t.Errorf("restored history missing %q:\n%s", want, joined)
		}
	}
}

// A session's worktree binding is a property of the session, recorded in the
// store - not something only the /resume LISTING knows. Every entry point that
// resolves a bare session id must therefore reach the same bound session:
// Mount (the UI's background mount and the remote/chat-sync path, which has no
// listing row at all) and the pool's own GetOrCreate. Before this, both took
// the plain loader, which filters instance_id IS NULL and cannot see a
// worktree session at all - so remote resume of a worktree session was
// impossible.
func TestBareIDEntryPointsResolveWorktreeBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resume func(boundResumeFixture) (ports.Conversation, error)
	}{
		{"Mount", func(f boundResumeFixture) (ports.Conversation, error) { return f.runner.Mount(f.seedID) }},
		{"GetOrCreate", func(f boundResumeFixture) (ports.Conversation, error) { return f.pool.GetOrCreate(f.seedID) }},
		{"SelectSession", func(f boundResumeFixture) (ports.Conversation, error) {
			out := f.runner.SelectSession(context.Background(), f.seedID)
			if out.Err != "" {
				return nil, errString(out.Err)
			}
			return out.Conversation, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBoundResumeFixture(t)
			conv, err := tc.resume(f)
			if err != nil {
				t.Fatalf("resume by bare id: %v", err)
			}
			assertRestoredWorktreeHistory(t, conv)
			sess := f.pool.Session(f.seedID)
			if sess == nil {
				t.Fatal("pool has no session under the resumed id")
			}
			if got := sess.ContextWorktreeBinding(); got != f.instance {
				t.Fatalf("resumed session binding = %+v, want %+v - an unbound resume writes its next turn outside the worktree", got, f.instance)
			}
		})
	}
}

// Fail closed, never silently unbound: once the worktree is being deleted, a
// bare-id resume must refuse rather than mount the session detached from the
// worktree it belongs to and let its next turn commit against the main
// checkout.
func TestBareIDResumeFailsClosedWhenWorktreeGone(t *testing.T) {
	f := newBoundResumeFixture(t)
	principal, err := worktreeroute.Principal(f.mainDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginWorktreeDeletion(context.Background(), principal, f.instance); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	if _, err := f.runner.Mount(f.seedID); err == nil {
		t.Fatal("Mount of a session in a deleted worktree succeeded, want a fail-closed error")
	}
	if _, err := f.pool.GetOrCreate(f.seedID); err == nil {
		t.Fatal("GetOrCreate of a session in a deleted worktree succeeded, want a fail-closed error")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// The store, not the tool registry, is what knows a session's worktree
// binding. Resolving it through the tool-scoped donor meant that with tools
// off (`mivia chat --no-tools`) no pooled member qualified, every bare-id
// resume silently took the plain loader, and a worktree session became
// unreachable again - the exact failure this resolution exists to prevent.
func TestBareIDResumeResolvesWorktreeBindingWithToolsOff(t *testing.T) {
	f := newBoundResumeFixtureWithTools(t, false)

	conv, err := f.pool.GetOrCreate(f.seedID)
	if err != nil {
		t.Fatalf("bare-id resume with tools off: %v", err)
	}
	assertRestoredWorktreeHistory(t, conv)
	if got := f.pool.Session(f.seedID).ContextWorktreeBinding(); got != f.instance {
		t.Fatalf("resumed session binding = %+v, want %+v", got, f.instance)
	}
}

// A resume that finishes after ReleaseLeases has drained the pool must not
// publish: adoptWorktreeToolsLocked releases p.mu around its registry build,
// so a shutdown can latch and drain inside that window. Publishing afterwards
// left a session holding a renewing lease that nothing releases, and the next
// process's resume of that id is refused as "live in another process" for the
// full TTL.
func TestResumeCompletingAfterReleaseLeasesDoesNotPublish(t *testing.T) {
	f := newBoundResumeFixture(t)
	f.pool.ReleaseLeases(context.Background())

	conv, err := f.pool.GetOrCreate(f.seedID)
	if err == nil {
		t.Fatalf("resume after the pool was drained returned a conversation %v, want a refusal", conv)
	}
	if got := f.pool.Session(f.seedID); got != nil {
		t.Fatal("a session was published into a drained pool: its lease is never released")
	}
}
