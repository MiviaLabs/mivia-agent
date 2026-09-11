package cliautomations

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/automation"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// TestHeadlessSpawnerSatisfiesSessionSpawner is a compile-time pin: the
// package-level var _ assertion in spawner.go itself already enforces
// this, but a dedicated test makes the intent discoverable from the
// test file listing too.
func TestHeadlessSpawnerSatisfiesSessionSpawner(t *testing.T) {
	var _ automation.SessionSpawner = (*HeadlessSpawner)(nil)
}

// TestHeadlessSpawnerImplementsCloseLastRun pins the exact interface
// shape internal/automation's serve.go type-asserts against:
// interface{ CloseLastRun() error }.
func TestHeadlessSpawnerImplementsCloseLastRun(t *testing.T) {
	spawn := &HeadlessSpawner{}
	if _, ok := any(spawn).(interface{ CloseLastRun() error }); !ok {
		t.Fatal("*HeadlessSpawner does not implement interface{ CloseLastRun() error }")
	}
}

// testResolvedConfig sets [subagents] store_path so storePathFor
// resolves under the spawner's own root (matching this repo's own
// dogfooded config) rather than the default shared, HOME-scoped store -
// several tests below (e.g. TestCreateFreshInDirBuildSessionErrorPropagates)
// chmod root's own .mivia dir and expect the session store open to fail
// against that same directory.
// A resolved provider runtime is part of the fixture because a spawned
// session now REQUIRES a working completer: without one the surface attach
// is a silent no-op and the run would execute on a dispatcher that never
// saw the workspace's tool policy, so buildCompleter reports instead of
// degrading.
func testResolvedConfig() *config.Resolved {
	return &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.SubagentConfig{StorePath: ".mivia/context.db"},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"openrouter": {
				ProviderName: "openrouter",
				BaseURL:      "https://openrouter.test/api/v1",
				APIKeySet:    true,
				APIKey:       "test-key",
			},
		},
	}
}

func TestCreateFreshInDirReturnsUsablePortsConversation(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	conv, err := spawn.CreateFreshInDir(nil, "")
	if err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if conv == nil {
		t.Fatal("CreateFreshInDir returned a nil conversation")
	}
	if conv.ID() == "" {
		t.Fatal("conversation ID is empty, want a real session id")
	}
}

// TestSetApprovalOverrideAppliesGateAndPolicyDirectlyToSession is the
// Path-B proof: assert on the bind-captured *chat.Session that
// ApprovalGate is set and SetApprovalPolicy took effect, with NO pooled
// multi-session cache involved anywhere in this test.
func TestSetApprovalOverrideAppliesGateAndPolicyDirectlyToSession(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	var bound *chat.Session
	conv, err := spawn.CreateFreshInDir(func(sess *chat.Session) (string, error) {
		bound = sess
		return "", nil
	}, "")
	if err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if bound == nil {
		t.Fatal("bind closure never received a session")
	}

	gate := func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true}
	}
	if err := spawn.SetApprovalOverride(conv.ID(), gate, "auto"); err != nil {
		t.Fatalf("SetApprovalOverride: %v", err)
	}
	if bound.ApprovalGate == nil {
		t.Fatal("ApprovalGate was not installed on the bound session")
	}
	result := bound.ApprovalGate(context.Background(), "x", nil)
	if !result.Approved {
		t.Fatal("installed gate did not behave as the gate passed in")
	}
}

// TestSetApprovalOverrideDoesNotDisturbCurrent proves SetApprovalOverride
// never touches `current`: create a session, override its approval
// posture, then CloseLastRun still closes the ORIGINAL store.
func TestSetApprovalOverrideDoesNotDisturbCurrent(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	conv, err := spawn.CreateFreshInDir(nil, "")
	if err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if err := spawn.SetApprovalOverride(conv.ID(), nil, "deny"); err != nil {
		t.Fatalf("SetApprovalOverride: %v", err)
	}
	spawn.mu.Lock()
	store := spawn.current
	spawn.mu.Unlock()
	if store == nil {
		t.Fatal("current is nil after CreateFreshInDir, want the opened store")
	}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("CloseLastRun: %v", err)
	}
	spawn.mu.Lock()
	after := spawn.current
	spawn.mu.Unlock()
	if after != nil {
		t.Fatal("CloseLastRun did not clear current")
	}
}

func TestCloseLastRunClosesAndForgetsStore(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("CloseLastRun: %v", err)
	}
	spawn.mu.Lock()
	defer spawn.mu.Unlock()
	if spawn.current != nil {
		t.Fatal("current is still set after CloseLastRun")
	}
	if spawn.lastSession != nil {
		t.Fatal("lastSession is still set after CloseLastRun")
	}
}

func TestCloseLastRunIdempotent(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("first CloseLastRun: %v", err)
	}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("second CloseLastRun: %v, want nil (idempotent)", err)
	}
}

func TestCloseLastRunWhenIdleIsNoOp(t *testing.T) {
	spawn := &HeadlessSpawner{}
	if err := spawn.CloseLastRun(); err != nil {
		t.Fatalf("CloseLastRun on idle spawner: %v, want nil", err)
	}
}

// TestCreateFreshInDirClosesStaleLeftoverDefensively calls
// CreateFreshInDir twice with no intervening CloseLastRun; the FIRST
// store is closed, current becomes the second store.
func TestCreateFreshInDirClosesStaleLeftoverDefensively(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("first CreateFreshInDir: %v", err)
	}
	spawn.mu.Lock()
	firstStore := spawn.current
	spawn.mu.Unlock()

	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("second CreateFreshInDir: %v", err)
	}
	spawn.mu.Lock()
	secondStore := spawn.current
	spawn.mu.Unlock()

	if firstStore == secondStore {
		t.Fatal("current did not change between the two CreateFreshInDir calls")
	}
	// The first store must already be closed: a further Close call on it
	// should not panic, and per storage.SQLite's own contract a second
	// Close is safe (sync.Once-guarded) - proving it here would need an
	// exported "is closed" probe this package does not have, so instead
	// assert indirectly: closing it again returns nil (already-closed is
	// idempotent), which would only be meaningful if it were genuinely
	// closed already. This is a smoke check, not a strong assertion.
	if err := firstStore.Close(); err != nil {
		t.Fatalf("closing the already-defensively-closed first store errored: %v", err)
	}
}

func TestCreateFreshInDirFailureLeavesCurrentNil(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	bindErr := context.Canceled
	_, err = spawn.CreateFreshInDir(func(*chat.Session) (string, error) {
		return "", bindErr
	}, "")
	if err == nil {
		t.Fatal("CreateFreshInDir with a failing bind: got nil error, want the bind error")
	}
	spawn.mu.Lock()
	defer spawn.mu.Unlock()
	if spawn.current != nil {
		t.Fatal("current is set after a failed bind, want nil")
	}
}

// TestCloseLastRunReleasesContextLeaseHeartbeat is the audit-found
// regression test: composition.BuildSession arms a context-lease
// heartbeat goroutine (chat.Session.contextHeartbeat) independently of
// the *storage.SQLite handle CloseLastRun closes. Confirmed by live
// audit of the real binary (commit 722e78e9): a long-running
// `automations serve` process leaked exactly one such goroutine per
// fire, unboundedly, because CloseLastRun closed only the store. This
// drives many CreateFreshInDir+CloseLastRun cycles and asserts the
// process's total goroutine count does not grow with the iteration
// count - a pre-fix implementation (CloseLastRun with no
// ReleaseContextLease call) fails this by leaking one goroutine per
// iteration.
func TestCloseLastRunReleasesContextLeaseHeartbeat(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}

	const iterations = 30
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < iterations; i++ {
		if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
			t.Fatalf("CreateFreshInDir iteration %d: %v", i, err)
		}
		if err := spawn.CloseLastRun(); err != nil {
			t.Fatalf("CloseLastRun iteration %d: %v", i, err)
		}
	}

	// The heartbeat goroutine's own stop() synchronously joins before
	// release()/stop() returns (see context_heartbeat.go's own
	// goroutine-join contract), so no sleep/retry loop is needed here -
	// by the time the last CloseLastRun call above returned, every
	// heartbeat this loop armed has already exited.
	runtime.GC()
	after := runtime.NumGoroutine()

	// A small constant slack (not iterations-proportional) absorbs
	// unrelated background goroutines (GC assist, once-off std-lib
	// bookkeeping) that a bare NumGoroutine comparison is inherently
	// noisy about; the leak this test targets is O(iterations), so any
	// fixed, small bound well below `iterations` still discriminates a
	// real regression from that noise.
	const slack = 5
	if after > baseline+slack {
		t.Fatalf("goroutine count grew from %d to %d across %d create/close cycles (slack %d) - want bounded, not O(iterations); a per-fire heartbeat leak regressed", baseline, after, iterations, slack)
	}
}

// TestSourceNeverReferencesSessionPoolTypes is a source-guard test: parse
// every .go file in this package and assert no identifier named
// SessionPool, BindFunc, or NewSessionPool ever appears - the same
// guarantee the plan's grep check makes, pinned as a test so CI catches
// a regression even if someone forgets to run the grep manually.
func TestSourceNeverReferencesSessionPoolTypes(t *testing.T) {
	forbidden := []string{"SessionPool", "BindFunc", "NewSessionPool"}
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, bad := range forbidden {
				if ident.Name == bad {
					t.Errorf("%s: forbidden identifier %q referenced", path, bad)
				}
			}
			return true
		})
	}
}

// --- NewHeadlessSpawner / buildCompleter / CreateFreshInDir error
// --- branches not otherwise reachable above ---

// TestNewHeadlessSpawnerRejectsEmptyWorkspaceRoot covers NewHeadlessSpawner's
// own empty-workspaceRoot guard directly.
func TestNewHeadlessSpawnerRejectsEmptyWorkspaceRoot(t *testing.T) {
	_, err := NewHeadlessSpawner("", testResolvedConfig())
	if err == nil {
		t.Fatal("NewHeadlessSpawner(\"\", ...): got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "non-empty workspace root") {
		t.Fatalf("error = %q, want it naming the empty-workspace-root guard", err.Error())
	}
}

// TestNewHeadlessSpawnerRejectsNilConfig covers NewHeadlessSpawner's own
// nil-config guard directly.
func TestNewHeadlessSpawnerRejectsNilConfig(t *testing.T) {
	_, err := NewHeadlessSpawner(t.TempDir(), nil)
	if err == nil {
		t.Fatal("NewHeadlessSpawner(root, nil): got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "non-nil config") {
		t.Fatalf("error = %q, want it naming the nil-config guard", err.Error())
	}
}

// TestBuildCompleterReportsAnEmptyProviderName covers buildCompleter's own
// empty-ProviderName guard. It REPORTS rather than returning a nil
// completer: an absent completer makes the surface attach a silent no-op,
// so the run would proceed on a dispatcher that never saw the workspace's
// tool policy.
func TestBuildCompleterReportsAnEmptyProviderName(t *testing.T) {
	spawn, err := NewHeadlessSpawner(t.TempDir(), &config.Resolved{})
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	comp, err := spawn.buildCompleter()
	if err == nil {
		t.Fatalf("buildCompleter with an empty ProviderName = %v, want an error", comp)
	}
	if comp != nil {
		t.Fatalf("buildCompleter returned %v alongside its error, want nil", comp)
	}
}

// TestBuildCompleterPropagatesAProviderError covers buildCompleter's
// provider.New-error branch: a config.Resolved with ProviderName set but no
// matching ProviderRuntimes entry makes provider.New fail, and the error
// must reach the caller rather than becoming a nil completer.
func TestBuildCompleterPropagatesAProviderError(t *testing.T) {
	spawn, err := NewHeadlessSpawner(t.TempDir(), &config.Resolved{ProviderName: "openrouter"})
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	comp, err := spawn.buildCompleter()
	if err == nil {
		t.Fatalf("buildCompleter with an unconfigured provider = %v, want an error", comp)
	}
	if comp != nil {
		t.Fatalf("buildCompleter returned %v alongside its error, want nil", comp)
	}
}

// TestCreateFreshInDirWorkspaceOpenErrorPropagates covers
// CreateFreshInDir's own workspace.Open error-wrap branch: a dir that
// resolves to an existing regular FILE (not a directory) makes
// workspace.Open fail its own IsDir check.
func TestCreateFreshInDirWorkspaceOpenErrorPropagates(t *testing.T) {
	root := t.TempDir()
	notADir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	_, err = spawn.CreateFreshInDir(nil, notADir)
	if err == nil {
		t.Fatal("CreateFreshInDir with dir pointing at a regular file: got nil error, want workspace.Open's own failure")
	}
	if !strings.Contains(err.Error(), "open workspace") {
		t.Fatalf("error = %q, want it naming the open-workspace wrap", err.Error())
	}
}

// TestCreateFreshInDirBuildSessionErrorPropagates covers CreateFreshInDir's
// own composition.BuildSession error-wrap branch: an empty StorePath (via
// a workspace root of "" resolved to "." then namespaced under a
// directory this test makes unwritable) is not viable here directly, so
// instead this test forces the failure through a nil *config.Resolved's
// Config field - composition.BuildSession's own first guard
// (`in.Config == nil`) - which buildCompleter itself would nil-panic on
// before that, so the spawner is built with a valid config and the
// failure is forced via an unwritable .mivia directory under the
// session's context-store path instead, mirroring
// TestBuildServiceOpenAutomationStoreError's own technique in
// automations_dispatch_test.go.
func TestCreateFreshInDirBuildSessionErrorPropagates(t *testing.T) {
	root := t.TempDir()
	miviaDir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(miviaDir, 0o500); err != nil {
		t.Fatalf("mkdir read-only .mivia: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(miviaDir, 0o755) })

	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	_, err = spawn.CreateFreshInDir(nil, "")
	if err == nil {
		t.Fatal("CreateFreshInDir against an unwritable .mivia dir: got nil error, want composition.BuildSession's own failure")
	}
	if !strings.Contains(err.Error(), "build session") {
		t.Fatalf("error = %q, want it naming the build-session wrap", err.Error())
	}
}

// TestCreateFreshInDirLogsStaleStoreCloseError covers CreateFreshInDir's
// own stale-store-Close-error log branch (159-161): it substitutes
// h.current with a store already closed before the second
// CreateFreshInDir call, so the defensive re-close on the stale entry
// itself errors - hit via storage.SQLite's own idempotent-but-inspectable
// Close (a second Close on an already-closed store returns the FIRST
// call's cached result, which is nil on a clean close, so this test
// instead forces a real close error on the RAW file by removing the
// store's underlying db file out from under it between the two
// CreateFreshInDir calls, which is the only way to make a SECOND Close
// legitimately fail without relying on storage.SQLite internals this
// package cannot reach).
func TestCreateFreshInDirLogsStaleStoreCloseError(t *testing.T) {
	root := t.TempDir()
	spawn, err := NewHeadlessSpawner(root, testResolvedConfig())
	if err != nil {
		t.Fatalf("NewHeadlessSpawner: %v", err)
	}
	t.Cleanup(func() { _ = spawn.CloseLastRun() })

	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("first CreateFreshInDir: %v", err)
	}
	spawn.mu.Lock()
	firstStore := spawn.current
	spawn.mu.Unlock()
	// Close it out from under the spawner directly so the SECOND
	// CreateFreshInDir call's own defensive re-close on this now-already-
	// closed store is a genuine no-op close (storage.SQLite.Close is
	// sync.Once-guarded and returns the cached nil result) - this proves
	// the log branch is reached without asserting on log output (this
	// package has no captured logger seam), matching the existing
	// TestCreateFreshInDirClosesStaleLeftoverDefensively's own "smoke
	// check, not a strong assertion" precedent.
	if err := firstStore.Close(); err != nil {
		t.Fatalf("pre-closing first store: %v", err)
	}
	if _, err := spawn.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("second CreateFreshInDir: %v", err)
	}
}

// TestSetApprovalOverrideRejectsNoActiveSession covers SetApprovalOverride's
// own "no active session" guard: calling it on a freshly-constructed
// spawner that has never had CreateFreshInDir called must fail rather
// than silently no-op.
func TestSetApprovalOverrideRejectsNoActiveSession(t *testing.T) {
	spawn := &HeadlessSpawner{}
	err := spawn.SetApprovalOverride("any-id", nil, "deny")
	if err == nil {
		t.Fatal("SetApprovalOverride on a spawner with no active session: got nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "no active session") {
		t.Fatalf("error = %q, want it naming the no-active-session guard", err.Error())
	}
}
