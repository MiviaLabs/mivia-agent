package uiadapter

// White-box coverage for the InDir creators' root-scoping contract: which
// paths keep the inherited launch registry byte-for-byte, which rebuild,
// memo/symlink collapse, fullDisk posture mirroring, and CloseAll
// lifecycle. Builds run against plain tempdirs - no git needed here
// (canonicalRepoRoot's vcs probe fails closed to the worktree dir itself),
// and the workflow wiring seam is stubbed so nothing depends on
// internal/cli being linked into this test binary.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

type noopTool struct{}

func (noopTool) Name() string               { return "noop" }
func (noopTool) Description() string        { return "does nothing" }
func (noopTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (noopTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

// stubWorkflowWiring neutralizes the cli-wired seam for this binary slice;
// each test that touches it restores the previous value.

func stubWorkflowWiring(t *testing.T) {
	t.Helper()
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })
}

func baseRegistryAt(t *testing.T, root string, fullDisk bool) *tools.Registry {
	t.Helper()
	var ws *workspace.Root
	var err error
	if fullDisk {
		ws, err = workspace.OpenFullDisk(root)
	} else {
		ws, err = workspace.Open(root)
	}
	if err != nil {
		t.Fatalf("open workspace %s: %v", root, err)
	}
	return tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
}

func newPoolAtRoot(t *testing.T, launchRoot string) (*SessionPool, *chat.Session, *config.Resolved) {
	t.Helper()
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-main"
	pool := NewSessionPool(sess, res, nil, true)
	sess.Tools = baseRegistryAt(t, launchRoot, false)
	return pool, sess, res
}

func otherRoot(t *testing.T, base string) string {
	t.Helper()
	dir := filepath.Join(base, "wt-x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func TestInDir_LaunchRootAndEmptyDirKeepInheritedRegistry(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools

	conv, err := pool.CreateFreshInDir(nil, rootA)
	if err != nil {
		t.Fatalf("CreateFreshInDir(launch root): %v", err)
	}
	fresh := conv.(*Conversation).Session()
	if fresh.Tools != inherited {
		t.Fatal("launch-root entry rebuilt its registry instead of inheriting")
	}

	if _, err := pool.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("CreateFreshInDir(empty): %v", err)
	}
	last := pool.lastCreated.Session()
	if last.Tools != inherited {
		t.Fatal("empty-dir entry changed tool identity")
	}
	if got := len(pool.regCloses); got != 0 {
		t.Fatalf("no-build paths registered %d closers, want 0", got)
	}
}

func TestInDir_RebuildsMemoizesAndCollapsesSymlinks(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, rootA)
	inherited := pool.sessions["session-main"].Tools

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("CreateFreshInDir(B): %v", err)
	}
	first := pool.lastCreated.Session().Tools
	if first == inherited || first == nil {
		t.Fatal("worktree entry kept the inherited registry")
	}
	if got := first.WorkspaceRoot(); got != filepath.Clean(wtB) {
		t.Fatalf("rebuilt root=%q, want %q", got, filepath.Clean(wtB))
	}
	if first.WorkspaceUnrestricted() {
		t.Fatal("fullDisk=false rebuild produced an unrestricted registry")
	}
	if got := len(pool.regCloses); got != 1 {
		t.Fatalf("closers=%d, want 1", got)
	}

	// Same canonical root shares the memoized pointer; a symlinked
	// spelling of the same directory collapses onto it too.
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("second CreateFreshInDir(B): %v", err)
	}
	if pool.lastCreated.Session().Tools != first {
		t.Fatal("same-root entries did not share the memoized registry")
	}
	link := filepath.Join(t.TempDir(), "wt-b-link")
	if err := os.Symlink(wtB, link); err != nil {
		return // platform without symlinks: collapse case untestable here
	}
	if _, err := pool.CreateFreshInDir(nil, link); err != nil {
		t.Fatalf("CreateFreshInDir(symlink): %v", err)
	}
	if pool.lastCreated.Session().Tools != first {
		t.Fatal("symlinked spelling produced a second registry")
	}
	if pool.regByRoot[filepath.Clean(wtB)] != first {
		t.Fatal("memo entry does not point at the shared rebuilt registry")
	}
}

func TestInDir_UnrestrictedPostureMirrorsLaunch(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-main"
	pool := NewSessionPool(sess, res, nil, true)
	sess.Tools = baseRegistryAt(t, rootA, true)

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("CreateFreshInDir under full-disk posture: %v", err)
	}
	rebuilt := pool.lastCreated.Session().Tools
	if !rebuilt.WorkspaceUnrestricted() {
		t.Fatal("full-disk posture did not carry into the rebuilt worktree registry")
	}
	if got := rebuilt.WorkspaceRoot(); got != filepath.Clean(wtB) {
		t.Fatalf("rebuilt root=%q, want %q", got, filepath.Clean(wtB))
	}
}

func TestInDir_CloseAllDropsMemoAndTriggersRealRebuild(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, rootA)

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("first CreateFreshInDir: %v", err)
	}
	memoized := pool.regByRoot[filepath.Clean(wtB)]

	pool.CloseAll()
	if pool.regByRoot != nil || len(pool.regCloses) != 0 {
		t.Fatal("CloseAll left memo state behind")
	}

	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("post-CloseAll CreateFreshInDir: %v", err)
	}
	if fresh := pool.lastCreated.Session().Tools; fresh == memoized {
		t.Fatal("post-CloseAll entry reused the released registry")
	}
	if got := len(pool.regCloses); got != 1 {
		t.Fatalf("closers after rebuild=%d, want 1", got)
	}
}

func TestInDir_HelpersHandleNilToolsMembersAndNonGitRoots(t *testing.T) {
	stubWorkflowWiring(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}

	// Empty pool: the unrestricted probe falls through to false.
	empty := NewSessionPool(nil, res, nil, true)
	if empty.anyMemberUnrestricted() {
		t.Fatal("empty pool reported unrestricted")
	}

	// Members without registries are skipped, not dereferenced.
	pool := NewSessionPool(nil, res, nil, true)
	toolless := chat.NewSession(res, nil)
	toolless.SessionID = "toolless"
	pool.sessions[toolless.SessionID] = toolless
	if pool.anyMemberUnrestricted() {
		t.Fatal("nil-tools member reported unrestricted")
	}
	if got := pool.launchRootLocked(); got != "" {
		t.Fatalf("launch root %q derived from a tool-less member", got)
	}

	// Non-git worktree dirs fail the vcs probe and fall back to themselves
	// as the memory root - DegradedIsolationByDesign, never an empty path.
	outside := filepath.Join(t.TempDir(), "outside-wt")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalRepoRoot(outside); got != outside {
		t.Fatalf("canonicalRepoRoot fallback=%q, want the input dir", got)
	}

	// This checkout IS a git repository: probing a nested directory must
	// find the MAIN root rather than take the local-fallback arm.
	// This branch lives in a git WORKTREE: its main root is the primary
	// checkout (.../mivia-agent), while "." here is .../mivia-agent-tui-
	// sessions/internal. Probing a nested dir must find that main root,
	// never take the local fallback.
	nested := cwdOr(t) // this package dir lives inside the feature worktree
	got := canonicalRepoRoot(nested)
	mainRoot, rootErr := worktreeroute.Root(nested)
	if rootErr != nil {
		t.Fatalf("resolve main root for %s: %v", nested, rootErr)
	}
	if samePath(got, nested) {
		t.Fatalf("canonicalRepoRoot(%s) took the local fallback inside a git checkout", nested)
	}
	if !samePath(got, mainRoot) {
		// Exact equality, not a string-prefix check: a prefix comparison
		// passed here by lexical accident whenever the worktree directory
		// name extended the main checkout's name.
		t.Fatalf("canonicalRepoRoot(%s)=%q, want the main root %q", nested, got, mainRoot)
	}
}

func cwdOr(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestInDir_BuilderFailureKeepsInheritedTools(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools

	// Prime once (memoized), then remove the directory: the rebuild for a
	// second entry fails workspace.Open and keeps inherited tools without
	// disturbing the pool state or the memo entry it still cannot satisfy.
	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("prime CreateFreshInDir: %v", err)
	}
	memoized := pool.regByRoot[filepath.Clean(wtB)]
	pool.CloseAll()

	os.RemoveAll(wtB)
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = nil // fail loud behind the seam
	cliagents.WireWorkflowToolOptionsVar = func(
		d *tools.DefaultOptions, _ string, _ *config.Resolved, _ func() *events.Bus, _ bool, _ ledger.LedgerRepository,
	) {
		d.Workspace = &workspace.Root{}
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	// workspace.Open ran before the seam; force its failure instead by
	// asking for the now-removed root.
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("creation must survive builder failure: %v", err)
	}
	last := pool.lastCreated.Session()
	if last.Tools != inherited && last.Tools != memoized {
		t.Fatalf("builder failure changed tool identity unexpectedly")
	}
}

func TestInDir_BindFailuresWrapAndAbort(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools
	boom := &exitErrorStub{"worktree gone"}

	if _, err := pool.CreateFreshInDir(func(*chat.Session) (string, error) { return "", boom }, ""); err == nil || !strings.Contains(err.Error(), "bind fresh session") {
		t.Fatalf("fresh bind error = %v, want wrapped bind failure", err)
	}
	if _, err := pool.GetOrCreateInDir("session-missing", func(*chat.Session) (string, error) { return "", boom }, ""); err == nil || !strings.Contains(err.Error(), `bind session "session-missing"`) {
		t.Fatalf("resume bind error = %v, want wrapped bind failure", err)
	}
	if pool.lastCreated != nil {
		t.Fatal("aborted bind left a lastCreated behind")
	}
	if pool.sessions["session-main"].Tools != inherited {
		t.Fatal("pool state disturbed by aborted binds")
	}
}

type exitErrorStub struct{ msg string }

func (e *exitErrorStub) Error() string { return e.msg }

func TestInDir_RelativeDirKeepsInheritedSilently(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools

	conv, err := pool.CreateFreshInDir(nil, "relative/dir")
	if err != nil {
		t.Fatalf("relative dir must not fail creation: %v", err)
	}
	if conv.(*Conversation).Session().Tools != inherited {
		t.Fatal("unresolvable dir changed tool identity")
	}
}

func TestInDir_BuilderFailureMidInheritKeepsInherited(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools

	// A REGULAR FILE passes symlink canonicalization but fails
	// workspace.Open - forcing the builder-failure arm without touching
	// the wiring seam.
	wtFile := filepath.Join(t.TempDir(), "wt-as-file")
	if err := os.WriteFile(wtFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.CreateFreshInDir(nil, wtFile); err != nil {
		t.Fatalf("builder failure must not abort creation: %v", err)
	}
	last := pool.lastCreated.Session()
	if last.Tools != inherited {
		t.Fatal("builder failure swapped in a non-inherited registry")
	}
	if len(pool.regCloses) != 0 || pool.regByRoot != nil {
		t.Fatal("failed build left memo/closer state behind")
	}
}

// TestInDir_MixedMembershipInheritsLaunchToolsDeterministically pins the
// P2 fix: once a worktree adoption puts a differently-rooted registry in
// the pool, later dir=="" creators (plain /new, typed /resume) must still
// inherit the LAUNCH member's registry - Go map iteration is randomized,
// so "first member" has to be an explicit pinned seed.
func TestInDir_MixedMembershipInheritsLaunchToolsDeterministically(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	launchRegistry := mainSess.Tools

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("worktree entry creation: %v", err)
	}
	worktreeEntry := pool.lastCreated.Session()
	if worktreeEntry.Tools == launchRegistry {
		t.Fatal("precondition broken: worktree entry kept the launch registry")
	}

	for i := range 12 { // sample the randomized map iteration repeatedly
		conv, err := pool.CreateFresh() // dir=="" plain path
		if err != nil {
			t.Fatalf("CreateFresh #%d: %v", i, err)
		}
		sess := conv.(*Conversation).Session()
		if sess.Tools != launchRegistry {
			t.Fatalf("iteration %d inherited a non-launch registry", i)
		}
		if got := pool.lastCreated.Session().Tools.WorkspaceRoot(); got != filepath.Clean(rootA) {
			t.Fatalf("iteration %d workspace root=%q, want %q", i, got, filepath.Clean(rootA))
		}
	}
}

func TestInDir_UnseededPoolsCoverLegacyFallbackArms(t *testing.T) {
	stubWorkflowWiring(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}

	// seedID empty + non-empty map: legacy first-map fallback picks a
	// member and inherits from it without panicking or mis-scoping.
	pool := NewSessionPool(nil, res, nil, true)
	stray := chat.NewSession(res, nil)
	stray.SessionID = "stray"
	stray.Tools = baseRegistryAt(t, t.TempDir(), false)
	pool.sessions[stray.SessionID] = stray

	conv, err := pool.CreateFreshInDir(nil, "")
	if err != nil {
		t.Fatalf("fallback-pool CreateFreshInDir: %v", err)
	}
	if sess := conv.(*Conversation).Session(); sess.Tools == nil ||
		sess.Tools.WorkspaceRoot() != stray.Tools.WorkspaceRoot() {
		t.Fatalf("fallback inheritance picked %v", sess.Tools)
	}

	// Empty map entirely: creation still succeeds; inheritance is a
	// silent no-op because there is nothing to inherit from.
	pool2 := NewSessionPool(nil, res, nil, true)
	if _, err := pool2.CreateFreshInDir(nil, ""); err != nil {
		t.Fatalf("empty-pool CreateFreshInDir: %v", err)
	}
	if s := pool2.lastCreated.Session(); s.Tools != nil {
		t.Fatal("empty pool fabricated a registry")
	}
}

func TestInDir_ConcurrentPublishLoserClosesAndAdopts(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, rootA)

	wtB := otherRoot(t, t.TempDir())
	var winnerReg *tools.Registry

	prev := cliagents.BuildToolsForRootHookForTest
	cliagents.BuildToolsForRootHookForTest = func(rws, _ string, _ bool, _ *config.Resolved) (*tools.Registry, func(), error) {
		// Simulate a concurrent winner publishing during the unlocked
		// build window: prime the memo, then hand back a loser registry.
		winnerReg = baseRegistryAt(t, rws, false)
		pool.mu.Lock()
		if pool.regByRoot == nil {
			pool.regByRoot = map[string]*tools.Registry{}
		}
		pool.regByRoot[filepath.Clean(rws)] = winnerReg
		pool.regCloses = append(pool.regCloses, func() {})
		pool.mu.Unlock()

		loser := baseRegistryAt(t, rws, false)
		return loser, func() {}, nil
	}
	t.Cleanup(func() { cliagents.BuildToolsForRootHookForTest = prev })

	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	if pool.regByRoot[filepath.Clean(wtB)] != winnerReg {
		t.Fatal("winner registry was overwritten by the loser")
	}
	// Loser's closer was invoked on the spot (handle released); the
	// winner's stays parked for CloseAll.
	if got := len(pool.regCloses); got != 1 {
		t.Fatalf("closers=%d, want 1 (winner only)", got)
	}
	if lastCreated := pool.lastCreated.Session().Tools; lastCreated != winnerReg {
		t.Fatal("session adopted the loser registry")
	}
}

func TestSelectWorktreeSummaryMatchesOnlyBoundRows(t *testing.T) {
	rows := []ports.SessionSummary{
		{ID: "plain-1", Title: "Plain"},
		{ID: "bound-wt1", Title: "Worktree Work", Worktree: "wt1", WorktreeInstanceID: "wt_0000000000000001"},
		{ID: "worktree:wt2", Title: "Worktree · wt2", Worktree: "wt2", WorktreeRoute: true},
		// Instance-less worktree metadata (legacy/unadopted rows) must not
		// route into the scoped creator: it fails on LiveWorktreeInstance
		// for worktrees storage never tracked, while plain resume works.
		{ID: "legacy-wt1", Title: "Old Work", Worktree: "wt1"},
	}
	cases := []struct {
		id     string
		wantOK bool
		wantWt string
	}{
		{"bound-wt1", true, "wt1"},
		{"worktree:wt2", false, ""},
		{"plain-1", false, ""},
		{"legacy-wt1", false, ""},
		{"unknown", false, ""},
	}
	for _, tc := range cases {
		got, ok := selectWorktreeSummary(rows, tc.id)
		if ok != tc.wantOK {
			t.Errorf("select(%q) ok=%v want %v", tc.id, ok, tc.wantOK)
			continue
		}
		if tc.wantOK && got.Worktree != tc.wantWt {
			t.Errorf("select(%q) worktree=%q want %q", tc.id, got.Worktree, tc.wantWt)
		}
	}
}

func TestSelectWorktreeSummary_SkipsRouteRowsEvenWhenIdMatchesPrefix(t *testing.T) {
	// Route pseudo-id "worktree:wt1" must NEVER match a real bound row
	// spelled "worktree:wt1..." in some other catalog, and a route row
	// must never match its own name: they start fresh, not resume.
	rows := []ports.SessionSummary{
		{ID: "worktree:wt10", Title: "Worktree · wt10", Worktree: "wt10", WorktreeRoute: true},
	}
	if _, ok := selectWorktreeSummary(rows, "worktree:wt1"); ok {
		t.Fatal("route pseudo-id matched an unrelated bound row by prefix")
	}
	if _, ok := selectWorktreeSummary(rows, "worktree:wt10"); ok {
		t.Fatal("route row matched itself; only plain rows are selectable")
	}
}

func TestInDir_ToolScopeNoticeOnCanonicalFailure(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	inherited := mainSess.Tools

	// Post-bind TOCTOU: bind validated the dir, but adoption re-resolves
	// it after the race window and finds it unresolvable. Creation must
	// still succeed on inherited tools, with the warning drained once.
	if _, err := pool.CreateFreshInDir(nil, "relative/dir"); err != nil {
		t.Fatalf("canonical-failure creation: %v", err)
	}
	if got := pool.takeToolScopeNotice(); !strings.Contains(got, toolScopeNotResolved) {
		t.Fatalf("notice = %q, want %q fragment", got, toolScopeNotResolved)
	}
	if second := pool.takeToolScopeNotice(); second != "" {
		t.Fatalf("notice did not drain; second read = %q", second)
	}
	if got := pool.lastCreated.Session().Tools; got != inherited {
		t.Fatal("failed adoption changed tool identity")
	}
}

func TestInDir_ToolScopeNoticeOnBuilderFailureKeepsInheritedTools(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, rootA)

	prevHook := cliagents.BuildToolsForRootHookForTest
	cliagents.BuildToolsForRootHookForTest = func(
		string, string, bool, *config.Resolved,
	) (*tools.Registry, func(), error) {
		return nil, func() {}, errors.New("boom-memory")
	}
	t.Cleanup(func() { cliagents.BuildToolsForRootHookForTest = prevHook })

	if _, err := pool.CreateFreshInDir(nil, otherRoot(t, t.TempDir())); err != nil {
		t.Fatalf("creation aborted by builder failure: %v", err)
	}
	got := pool.takeToolScopeNotice()
	if !strings.Contains(got, toolScopeRebuildFailedPrefix) || !strings.Contains(got, "boom-memory") {
		t.Fatalf("notice = %q, want rebuild-failed text carrying the error", got)
	}
}

func TestInDir_WorktreeRowsSafe_DegradesOnListingError(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, nil)
	pool := NewSessionPool(sess, res, nil, true)
	r := &CommandRunner{sess: sess, pool: pool}
	// No context store installed: listSessionSummaries fails, degrading
	// to nil without panicking.
	if rows := r.worktreeRowsSafe(); rows != nil {
		t.Fatalf("expected nil on listing error, got %d rows", len(rows))
	}
}

func TestInDir_AdoptedSessionCarriesPrivateBaseResolver(t *testing.T) {
	stubWorkflowWiring(t)
	rootA := t.TempDir()
	pool, mainSess, _ := newPoolAtRoot(t, rootA)
	launchTools := mainSess.Tools

	wtB := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wtB); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	adopted := pool.lastCreated.Session()
	if adopted.ToolBaseResolver == nil {
		t.Fatal("adopted session has no ToolBaseResolver")
	}
	base := adopted.ToolBaseResolver()
	if base == nil {
		t.Fatal("resolver returned nil")
	}
	// The resolver base is a private clone: distinct from both the launch
	// registry and the memoized worktree registry.
	memoized := pool.regByRoot[filepath.Clean(wtB)]
	if base == launchTools || base == memoized {
		t.Fatal("resolver shares a pointer with a live registry — growth would leak")
	}
	// Registering into the resolver base must not contaminate the memoized
	// registry that siblings adopt.
	base.Register(noopTool{})
	if _, found := memoized.Get("noop"); found {
		t.Fatal("resolver-base mutation leaked into the memoized registry")
	}
	// Unbound entries keep no resolver (launch fallback semantics).
	if _, err := pool.CreateFresh(); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	if plain := pool.lastCreated.Session(); plain.ToolBaseResolver != nil {
		t.Fatal("unbound entry received a resolver")
	}
}

// TestInDir_ConcurrentSameIDResumeJoinsTheWinner pins the post-relock
// recheck in getOrCreateInDirLocked: adoptWorktreeToolsLocked releases
// p.mu while a registry builds, and a racer resuming the SAME session id
// can fully register inside that window. The loser must JOIN the winner's
// conversation - loading a duplicate session would overwrite the map
// entries and orphan the winner outside ReleaseLeases' reach (the exact
// leaked-lease failure that function exists to prevent). Run under -race.
func TestInDir_ConcurrentSameIDResumeJoinsTheWinner(t *testing.T) {
	stubWorkflowWiring(t)
	dir := t.TempDir()
	wtDir := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{Model: "test-model"}
	seed := chat.NewSession(res, nil)
	seed.SessionID = "seed"
	seed.UseTools = true
	seed.Tools = tools.NewRegistry()

	saved := chat.NewSession(res, nil)
	saved.SessionID = "race-target"
	if err := saved.Save("race-target"); err != nil {
		t.Fatalf("seed saved session: %v", err)
	}

	pool := NewSessionPool(seed, res, nil, true)
	t.Cleanup(pool.CloseAll)

	firstBuild := make(chan struct{})   // closed when goroutine A is inside the builder
	releaseBuild := make(chan struct{}) // closed when A may finish building
	builds := 0
	prevHook := cliagents.BuildToolsForRootHookForTest
	cliagents.BuildToolsForRootHookForTest = func(string, string, bool, *config.Resolved) (*tools.Registry, func(), error) {
		builds++
		if builds == 1 {
			close(firstBuild)
			<-releaseBuild
		}
		return tools.NewRegistry(), func() {}, nil
	}
	t.Cleanup(func() { cliagents.BuildToolsForRootHookForTest = prevHook })

	type result struct {
		conv ports.Conversation
		err  error
	}
	resA := make(chan result, 1)
	go func() {
		conv, err := pool.GetOrCreateInDir("race-target", nil, wtDir)
		resA <- result{conv, err}
	}()
	<-firstBuild // A holds buildSer inside the builder; p.mu is free

	resB := make(chan result, 1)
	go func() {
		conv, err := pool.GetOrCreateInDir("race-target", nil, wtDir)
		resB <- result{conv, err}
	}()
	// Let B pass the convs-check (p.mu is free while A sits in the
	// builder) and queue on buildSer, then release A. The grace period
	// only makes the interleaving reliable; correctness of the assertion
	// does not depend on it.
	time.Sleep(100 * time.Millisecond)
	close(releaseBuild)

	a := <-resA
	b := <-resB
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent resumes errored: A=%v B=%v", a.err, b.err)
	}
	if a.conv != b.conv {
		t.Fatalf("same-id racers got different conversations: %p vs %p", a.conv, b.conv)
	}
	if got := pool.Session("race-target"); got == nil {
		t.Fatal("no session registered for the raced id")
	}
}

// TestInDir_BindReturnedRootWidensToolsAboveTheSessionSubdir pins item 1
// of the worktree-sessions follow-ups: a session SAVED in a worktree
// subdirectory must still get tools scoped to the worktree ROOT, the
// boundary StartInRoute already validated. The dir parameter carries the
// session's saved directory; the bind closure returns the validated root,
// and that return wins.
func TestInDir_BindReturnedRootWidensToolsAboveTheSessionSubdir(t *testing.T) {
	stubWorkflowWiring(t)
	launch := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, launch)
	t.Cleanup(pool.CloseAll)

	wtRoot := otherRoot(t, t.TempDir())
	subA := filepath.Join(wtRoot, "pkg", "a")
	subB := filepath.Join(wtRoot, "pkg", "b")
	for _, d := range []string{subA, subB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	bindToRoot := func(*chat.Session) (string, error) { return wtRoot, nil }

	convA, err := pool.CreateFreshInDir(bindToRoot, subA)
	if err != nil {
		t.Fatalf("CreateFreshInDir(subA): %v", err)
	}
	got := pool.Session(convA.ID()).Tools.WorkspaceRoot()
	if want := filepath.Clean(wtRoot); got != want {
		t.Errorf("tools scoped to %s, want the worktree root %s", got, want)
	}

	// A second session saved in a DIFFERENT subdirectory of the same
	// worktree must reuse the memoized registry, not build a second one.
	convB, err := pool.CreateFreshInDir(bindToRoot, subB)
	if err != nil {
		t.Fatalf("CreateFreshInDir(subB): %v", err)
	}
	if a, b := pool.Session(convA.ID()).Tools, pool.Session(convB.ID()).Tools; a != b {
		t.Errorf("two subdirs of one worktree built separate registries: %p vs %p", a, b)
	}
	if n := len(pool.regByRoot); n != 1 {
		t.Errorf("len(regByRoot) = %d, want 1 memo entry keyed on the worktree root", n)
	}
}

// TestInDir_BindWithoutARootKeepsTheDirParameter guards the fallback: a
// bind that returns no root (every non-worktree caller) must leave the
// dir-scoped behavior byte-for-byte unchanged.
func TestInDir_BindWithoutARootKeepsTheDirParameter(t *testing.T) {
	stubWorkflowWiring(t)
	launch := t.TempDir()
	pool, _, _ := newPoolAtRoot(t, launch)
	t.Cleanup(pool.CloseAll)

	wtB := otherRoot(t, t.TempDir())
	conv, err := pool.CreateFreshInDir(func(*chat.Session) (string, error) { return "", nil }, wtB)
	if err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	got := pool.Session(conv.ID()).Tools.WorkspaceRoot()
	if want := filepath.Clean(wtB); got != want {
		t.Errorf("tools scoped to %s, want the dir parameter %s", got, want)
	}
}
