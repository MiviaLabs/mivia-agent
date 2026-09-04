package uiadapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

// TestCommandRunner_ResumeSurfacesWorktreeRouteRows pins the list-side of
// the TUI worktree feature: storage's synthesized route pseudo-row must
// reach the picker as a ports.SessionSummary carrying the worktree name,
// route marker, directory, and the "Worktree · <name>" label.
func TestCommandRunner_ResumeSurfacesWorktreeRouteRows(t *testing.T) {
	store, _, mainDir, wtDir := worktreeCatalogFixture(t)
	sess, _ := catalogSession(t, store, mainDir)

	runner := uiadapter.NewCommandRunner(sess, &config.Resolved{}, nil)
	out := runner.Run(context.Background(), "resume", "")
	if out.Err != "" {
		t.Fatalf("/resume errored: %s", out.Err)
	}

	var found *ports.SessionSummary
	for i := range out.SessionChoices {
		if out.SessionChoices[i].Title == "Worktree · wt1" {
			found = &out.SessionChoices[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no Worktree · wt1 row in %+v", out.SessionChoices)
	}
	if !found.WorktreeRoute || found.Worktree != "wt1" || found.WorktreeDir != wtDir {
		t.Fatalf("route row metadata mismatch: %+v", found)
	}
	if found.ID != "worktree:wt1" {
		t.Fatalf("route row ID %q, want worktree:wt1", found.ID)
	}
}

// TestSessionPool_CreateFreshBound_BindsBeforeContextState pins the hook
// ordering SessionPool.CreateFreshBound exists for: a worktree binding is
// only acceptable while the fresh session carries no context state yet,
// and the inherited store lands afterwards.
func TestSessionPool_CreateFreshBound_BindsBeforeContextState(t *testing.T) {
	store, _, mainDir, canonicalWt := worktreeCatalogFixture(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	initial := chat.NewSession(res, &nullCompleter{})
	initial.SessionID = "session-main"
	principal, err0 := contextstate.NewPrincipal(worktreeroute.WorkspaceID(mainDir), initial.SessionID, "local-user")
	if err0 != nil {
		t.Fatalf("mint session principal: %v", err0)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := initial.SetContextManager(manager, principal); err != nil {
		t.Fatalf("seed initial context: %v", err)
	}
	if err := initial.SetContextStore(store); err != nil {
		t.Fatalf("seed initial store: %v", err)
	}

	pool := uiadapter.NewSessionPool(initial, res, nil, false)

	bindSawStore := false
	bindSawManager := false
	conv, err := pool.CreateFreshBound(func(sess *chat.Session) (string, error) {
		bindSawStore = sess.ContextStore() != nil
		bindSawManager = sess.ContextManager() != nil
		return startInRouteBind(context.Background(), store, mainDir, worktreeroute.Route{
			Worktree: "wt1",
			Dir:      canonicalWt,
		})(sess)
	})
	if err != nil {
		t.Fatalf("CreateFreshBound: %v", err)
	}
	if bindSawStore || bindSawManager {
		t.Fatal("bind ran after the inherited context state was installed")
	}
	if conv == nil || conv.ID() == "" || conv.ID() == "session-main" {
		t.Fatalf("pooled conversation missing or reused the initial ID: %+v", conv)
	}
	pooled := pool.Session(conv.ID())
	if pooled == nil || pooled.ContextStore() != store {
		t.Fatal("fresh session did not inherit the repository store")
	}
}

// TestSessionPool_CreateFreshBound_AbortsOnBindFailure keeps creation
// all-or-nothing: a failed binding must not leave a half-initialized entry
// in the pool.
func TestSessionPool_CreateFreshBound_AbortsOnBindFailure(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	initial := chat.NewSession(res, &nullCompleter{})
	initial.SessionID = "s0"
	pool := uiadapter.NewSessionPool(initial, res, nil, false)

	bindErr := errors.New("no such worktree")
	if _, err := pool.CreateFreshBound(func(*chat.Session) (string, error) { return "", bindErr }); !errors.Is(err, bindErr) {
		t.Fatalf("CreateFreshBound err = %v, want bindErr", err)
	}
	if pool.Session("s0") != initial {
		t.Fatal("pool lost its initial session after an aborted bind")
	}
}

// TestCommandRunner_StartInWorktree exercises the port action end to end
// offline: selecting the route row must start a NEW pooled conversation,
// switch the runner's active session to it, and fail closed for a
// worktree name storage does not track.
func TestCommandRunner_StartInWorktree(t *testing.T) {
	stubWorkflowWiring(t)
	store, _, mainDir, canonicalWt := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	sess, _ := catalogSession(t, store, mainDir)
	// Production sessions run with tools enabled; without it the pool
	// keeps every entry unscoped (adopt guard).
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)

	t.Chdir(mainDir)

	initialID := sess.SessionID
	runner := uiadapter.NewCommandRunner(sess, &config.Resolved{ProviderName: "fake", Model: "m1"}, nil)

	routeRow := ports.SessionSummary{
		ID:            "worktree:wt1",
		Title:         "Worktree · wt1",
		Worktree:      "wt1",
		WorktreeRoute: true,
		WorktreeDir:   canonicalWt,
	}
	out := runner.StartInWorktree(context.Background(), routeRow)
	if out.Err != "" {
		t.Fatalf("StartInWorktree errored: %s", out.Err)
	}
	if out.Conversation == nil || out.Conversation.ID() == "" || out.Conversation.ID() == initialID {
		t.Fatalf("outcome did not install a new conversation: %+v", out.Conversation)
	}
	// Root-scoping: the rebuilt registry must resolve inside wt1 so
	// run_command defaults and fs confinement land in the worktree, not
	// the launch checkout.
	pooled := runner.Pool().Session(out.Conversation.ID())
	if pooled == nil {
		t.Fatal("started session missing from the pool")
	}
	if got := pooled.Tools.WorkspaceRoot(); got != canonicalWt {
		t.Fatalf("pooled session tool root=%q, want %q", got, canonicalWt)
	}
	ghost := routeRow
	ghost.Worktree = "ghost"
	if out := runner.StartInWorktree(context.Background(), ghost); out.Err == "" {
		t.Fatal("StartInWorktree accepted an unmanaged worktree name")
	}
}

// TestCommandRunner_ResumeInWorktree_FailuresCoverTheResumeArm exercises
// the resume path's error shaping, which shares worktreeSessionOutcome
// with StartInWorktree but labels its failures "resume".
func TestCommandRunner_ResumeInWorktree_FailuresCoverTheResumeArm(t *testing.T) {
	store, _, mainDir, _ := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, &config.Resolved{ProviderName: "fake", Model: "m1"}, nil)
	out := runner.ResumeInWorktree(context.Background(), ports.SessionSummary{
		ID:          "bound-wt9",
		Title:       "Worktree · wt9",
		Worktree:    "wt9",
		WorktreeDir: "/repo/.mivia/worktrees/wt9",
	})
	if out.Err == "" || !strings.Contains(out.Err, `failed to resume session in worktree "wt9"`) {
		t.Fatalf("resume failure shaped wrong: %q", out.Err)
	}
}

// TestCommandRunner_WorktreeActionsFailOutsideARepository covers the
// repository-root resolution guard both worktree actions share: without a
// git repository under the process cwd the action reports the resolution
// failure instead of binding against a phantom root.
func TestCommandRunner_WorktreeActionsFailOutsideARepository(t *testing.T) {
	store, _, mainDir, _ := worktreeCatalogFixture(t)
	sess, _ := catalogSession(t, store, mainDir)
	runner := uiadapter.NewCommandRunner(sess, &config.Resolved{ProviderName: "fake", Model: "m1"}, nil)

	t.Chdir(t.TempDir()) // no git repository anywhere above

	out := runner.StartInWorktree(context.Background(), ports.SessionSummary{
		ID: "worktree:wt1", Worktree: "wt1", WorktreeRoute: true,
		WorktreeDir: "/repo/.mivia/worktrees/wt1",
	})
	if out.Err == "" || !strings.Contains(out.Err, "resolve repository root") {
		t.Fatalf("expected root-resolution failure, got %q", out.Err)
	}
}

// TestCommandRunner_WorktreeActionsNeedARepositoryContextStore pins the
// fail-closed message when the active session runs without a SQLite
// context store (the only carrier of worktree-route state).
func TestCommandRunner_WorktreeActionsNeedARepositoryContextStore(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, &nullCompleter{})
	sess.SessionID = "plain-session"
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	out := runner.StartInWorktree(context.Background(), ports.SessionSummary{
		ID: "worktree:wt1", Worktree: "wt1", WorktreeRoute: true,
		WorktreeDir: "/repo/.mivia/worktrees/wt1",
	})
	if out.Err == "" || !strings.Contains(out.Err, "repository context store") {
		t.Fatalf("expected store-missing failure, got %q", out.Err)
	}
}

// TestCommandRunner_WorktreeActionsWithoutPool pins the nil-pool guard the
// worktree actions share with handleNew: NewCommandRunnerWithPool accepts
// a caller-supplied pool, so nil must degrade to an outcome error rather
// than a panic mid-keypress.
func TestCommandRunner_WorktreeActionsWithoutPool(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	sess := chat.NewSession(res, &nullCompleter{})
	sess.SessionID = "s0"
	runner := uiadapter.NewCommandRunnerWithPool(sess, nil, res, nil)

	out := runner.StartInWorktree(context.Background(), ports.SessionSummary{ID: "worktree:wt1", Worktree: "wt1"})
	if out.Err == "" || !strings.Contains(out.Err, "no session pool") {
		t.Fatalf("expected no-pool failure, got %q", out.Err)
	}
}

// TestCommandRunner_WorktreeActionsWithoutActiveSession pins the first
// guard the worktree actions hit: a runner with no session reports it
// instead of touching pool or store state.
func TestCommandRunner_WorktreeActionsWithoutActiveSession(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, &config.Resolved{}, nil)
	out := runner.ResumeInWorktree(context.Background(), ports.SessionSummary{ID: "x", Worktree: "wt"})
	if out.Err == "" || !strings.Contains(out.Err, "no active session") {
		t.Fatalf("expected no-active-session failure, got %q", out.Err)
	}
}

// TestCommandRunner_ResumeInWorktree_DegradesToSelectSession pins the
// contract that a summary without worktree metadata resumes the plain way,
// so no caller can accidentally strip the binding semantics onto a normal
// session row.
func TestCommandRunner_ResumeInWorktree_DegradesToSelectSession(t *testing.T) {
	store, _, mainDir, _ := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)
	runner := uiadapter.NewCommandRunner(sess, &config.Resolved{ProviderName: "fake", Model: "m1"}, nil)

	if err := sess.Save("plain-target"); err != nil {
		t.Fatalf("save target session: %v", err)
	}
	out := runner.ResumeInWorktree(context.Background(), ports.SessionSummary{ID: "plain-target"})
	if out.Err != "" {
		t.Fatalf("degraded resume errored: %s", out.Err)
	}
	if got := out.Notice; got == "" || !strings.Contains(got, "plain-target") {
		t.Fatalf("notice %q does not reference the resumed id", got)
	}
}

// TestSessionPool_BoundEntriesInstallSurfaceWidener covers the
// agentState-guarded tail of both bound pool paths: a configured
// AgentSessionState must reach fresh and resumed entries alike.
func TestSessionPool_BoundEntryGuardArmsRunBeforePersistence(t *testing.T) {
	store, _, mainDir, canonicalWt := worktreeCatalogFixture(t)
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	state := &cliagents.AgentSessionState{}
	initial := chat.NewSession(res, &nullCompleter{})
	initial.SessionID = "session-main"
	pool := uiadapter.NewSessionPool(initial, res, state, false)

	bind := startInRouteBind(context.Background(), store, mainDir,
		worktreeroute.Route{Worktree: "wt1", Dir: canonicalWt})
	if _, err := pool.CreateFreshBound(bind); err != nil {
		t.Fatalf("CreateFreshBound: %v", err)
	}
	// A resumed ID storage does not know errors out of Load - the guard
	// arms (bind, inherit, adopt) all ran ahead of persistence without
	// wedging the pool. (The surface widener itself has no session-side
	// getter; installing it is pinned by the entrybase invariant test in
	// internal/cliagents.)
	if _, err := pool.GetOrCreateBound("missing-id", nil); err == nil {
		t.Fatal("GetOrCreateBound loaded an id that was never saved")
	}
}

func TestCommandRunner_ResumeInWorktree_PooledEntryFencedWhenInstanceGone(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir, canonicalWt := fx.Store, fx.MainDir, fx.WorktreeDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)
	seedBoundWorktreeSession(t, store, mainDir, canonicalWt, res)
	principal, err := worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	summary := ports.SessionSummary{ID: resumeSavedFirstName, Worktree: "wt1", WorktreeDir: canonicalWt}

	if out := runner.ResumeInWorktree(context.Background(), summary); out.Err != "" {
		t.Fatalf("first resume errored: %s", out.Err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt1", ID: resumeFixtureInstanceID}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	if _, err := store.DeleteWorktreeSessions(context.Background(), principal, instance); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	out := runner.ResumeInWorktree(context.Background(), summary)
	if out.Err == "" || !strings.Contains(out.Err, `worktree "wt1" was removed`) ||
		!strings.Contains(out.Err, `cannot resume session in worktree "wt1"`) {
		t.Fatalf("fence error = %q, want removed-instance text", out.Err)
	}
	if out.Conversation != nil {
		t.Error("fenced outcome must not install a conversation")
	}
}

func TestCommandRunner_SelectSession_TypedBoundIdStillFailsClosed(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir, canonicalWt := fx.Store, fx.MainDir, fx.WorktreeDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	seedBoundWorktreeSession(t, store, mainDir, canonicalWt, res)

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)

	// Canary table over BOTH typed shapes: a raw bound-row id and the
	// managed catalog key. Both fail closed under today's storage filters;
	// any future instance-row leak flips these red on purpose.
	for _, tc := range []struct {
		id      string
		substrs []string
	}{
		{resumeSavedFirstName, []string{"failed to resume session"}},
		{"worktree:wt1", []string{"failed to resume session"}},
	} {
		out := runner.SelectSession(context.Background(), tc.id)
		if out.Err == "" {
			t.Errorf("typed %q resumed something; want fail-closed", tc.id)
			continue
		}
		for _, want := range tc.substrs {
			if !strings.Contains(out.Err, want) {
				t.Errorf("typed %q error %q missing %q", tc.id, out.Err, want)
			}
		}
	}
}

func TestCommandRunner_SelectSession_RoutePseudoIdDoesNotStartFresh(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)

	out := runner.SelectSession(context.Background(), "worktree:wt1")
	if out.Err == "" {
		t.Fatal("typed route id started something")
	}
	if got := pool.Session("worktree:wt1"); got != nil {
		t.Fatalf("route pseudo-id materialized a pooled session")
	}
}

func TestCommandRunner_SelectSession_ListedBoundRowRoutesToScopedResume(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	// A second MANAGED worktree (wtX) so StartInRoute's live-instance
	// validation passes; the hook replaces only the registry build.
	wtX := filepath.Join(filepath.Dir(mainDir), ".mivia", "worktrees", "wtX")
	if err := os.MkdirAll(wtX, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalX, err := worktreeroute.CanonicalDir(wtX)
	if err != nil {
		t.Fatal(err)
	}
	principal2, perr := worktreeroute.Principal(mainDir)
	if perr != nil {
		t.Fatal(perr)
	}
	instX := contextstate.WorktreeInstance{Worktree: "wtX", ID: "wt_00000000000000ff"}
	if err := store.BeginWorktreeCreation(context.Background(), principal2, instX, canonicalX); err != nil {
		t.Fatalf("begin wtX creation: %v", err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal2, instX, canonicalX); err != nil {
		t.Fatalf("register wtX: %v", err)
	}

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	runner.SetSummariesFnForTest(func() ([]ports.SessionSummary, error) {
		return []ports.SessionSummary{{
			ID: "bound-wtX", Title: "Worktree Work",
			Worktree:           "wtX",
			WorktreeDir:        canonicalX,
			WorktreeInstanceID: instX.ID,
		}}, nil
	})
	cliagents.BuildToolsForRootHookForTest = func(rws string, _ string, _ bool, _ *config.Resolved) (*tools.Registry, func(), error) {
		reg := tools.NewRegistry()
		return reg, func() {}, nil
	}
	prevHook := cliagents.BuildToolsForRootHookForTest
	t.Cleanup(func() { cliagents.BuildToolsForRootHookForTest = prevHook })

	// Routing proof lives in the SCOPED wrapper text: plain SelectSession
	// would say `failed to resume session "x"` without the worktree phrase.
	out := runner.SelectSession(context.Background(), "bound-wtX")
	if out.Err == "" || !strings.Contains(out.Err, `failed to resume session in worktree "wtX"`) {
		t.Fatalf("expected scoped-creator routing, got %q", out.Err)
	}
}

// TestCommandRunner_SelectSession_InstancelessWorktreeRowResumesPlain pins
// the routing gate for rows that carry worktree metadata but no managed
// instance: legacy pre-instance sessions and sessions saved from an
// unadopted or marker-less worktree directory. The scoped creator fails on
// LiveWorktreeInstance for such rows ("worktree deleted" while the
// directory exists), while the plain path resumed them fine before the
// router existed - so they must keep the plain path.
func TestCommandRunner_SelectSession_InstancelessWorktreeRowResumesPlain(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir, wtDir := fx.Store, fx.MainDir, fx.WorktreeDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)
	runner.SetSummariesFnForTest(func() ([]ports.SessionSummary, error) {
		return []ports.SessionSummary{{
			ID: "legacy-wt1", Title: "Old Worktree Session",
			Worktree:    "wt1",
			WorktreeDir: wtDir,
			// No WorktreeInstanceID: storage never tracked an instance.
		}}, nil
	})

	// The id does not resolve to a stored snapshot, so plain resume fails -
	// but with the PLAIN wrapper. The scoped wrapper (`in worktree "wt1"`)
	// would prove the row was misrouted into StartInRoute.
	out := runner.SelectSession(context.Background(), "legacy-wt1")
	if out.Err == "" {
		t.Fatal("resume of a missing snapshot unexpectedly succeeded")
	}
	if strings.Contains(out.Err, "in worktree") {
		t.Fatalf("instance-less row was routed into the scoped creator: %q", out.Err)
	}
	if !strings.Contains(out.Err, `failed to resume session`) {
		t.Fatalf("plain resume wrapper missing: %q", out.Err)
	}
}

func TestCommandRunner_SelectSession_ListingErrorDegradesToPlainPath(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)
	runner.SetSummariesFnForTest(func() ([]ports.SessionSummary, error) {
		return nil, errors.New("store wedged")
	})

	out := runner.SelectSession(context.Background(), "missing-id")
	if out.Err == "" || !strings.Contains(out.Err, "failed to resume session") {
		t.Fatalf("degraded path lost its wrapper: %q", out.Err)
	}
}

func TestCommandRunner_ResumeInWorktree_UnboundPooledEntryPassesThrough(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)

	// Correct behavior for an UNBOUND pooled entry: the fence skips
	// (binding is zero), the cached conversation is returned untouched,
	// and no new session materializes.
	out := runner.ResumeInWorktree(context.Background(),
		ports.SessionSummary{ID: "session-main", Worktree: "wtB"})
	if out.Err != "" {
		t.Fatalf("unexpected error on unbound pooled resume: %s", out.Err)
	}
	if got := pool.Session("session-main"); got != sess {
		t.Fatal("pass-through did not return the original pooled session")
	}
}

// TestCommandRunner_StartInWorktree_FencesOutOfBandReplacedDir pins the
// physical-identity check the DB row cannot make (REPL parity): a worktree
// removed and recreated out-of-band at the same path leaves the instance
// row active, but the on-disk marker is gone or names another instance.
// The bind must fail with the marker named, not silently scope a session
// to foreign content.
func TestCommandRunner_StartInWorktree_FencesOutOfBandReplacedDir(t *testing.T) {
	stubWorkflowWiring(t)
	store, _, mainDir, wtDir := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	route := ports.SessionSummary{ID: "worktree:wt1", Worktree: "wt1", WorktreeDir: wtDir, WorktreeRoute: true}

	// Marker mismatched: same path, different instance - the replaced-dir
	// signature.
	writeTestWorktreeMarker(t, wtDir, contextstate.WorktreeInstance{Worktree: "wt1", ID: "wt_deadbeefdeadbeef"})
	out := runner.StartInWorktree(context.Background(), route)
	if out.Err == "" || !strings.Contains(out.Err, "marker") {
		t.Fatalf("mismatched marker not fenced: %q", out.Err)
	}

	// Marker missing entirely: plain directory recreated at the path.
	if err := os.Remove(filepath.Join(wtDir, ".mivia", "worktree-instance.json")); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	out = runner.StartInWorktree(context.Background(), route)
	if out.Err == "" || !strings.Contains(out.Err, "marker") {
		t.Fatalf("missing marker not fenced: %q", out.Err)
	}
}

func TestCommandRunner_ResumeInWorktree_PooledEntryChecksMarker(t *testing.T) {
	stubWorkflowWiring(t)
	store, _, mainDir, wtDir := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	route := ports.SessionSummary{ID: "worktree:wt1", Worktree: "wt1", WorktreeDir: wtDir, WorktreeRoute: true}
	started := runner.StartInWorktree(context.Background(), route)
	if started.Err != "" || started.Conversation == nil {
		t.Fatalf("start: %+v", started)
	}
	row := ports.SessionSummary{ID: started.Conversation.ID(), Worktree: "wt1", WorktreeDir: wtDir, WorktreeInstanceID: "wt_0001020304050607"}
	if err := os.Remove(filepath.Join(wtDir, ".mivia", "worktree-instance.json")); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	resumed := runner.ResumeInWorktree(context.Background(), row)
	if resumed.Err == "" || !strings.Contains(resumed.Err, "marker") {
		t.Fatalf("pooled resume accepted missing marker: %q", resumed.Err)
	}
	if resumed.Conversation != nil {
		t.Fatal("pooled resume returned a conversation after marker validation failed")
	}

	writeTestWorktreeMarker(t, wtDir, contextstate.WorktreeInstance{Worktree: "wt1", ID: "other-instance"})
	resumed = runner.ResumeInWorktree(context.Background(), row)
	if resumed.Err == "" || !strings.Contains(resumed.Err, "marker") {
		t.Fatalf("pooled resume accepted mismatched marker: %q", resumed.Err)
	}
	if resumed.Conversation != nil {
		t.Fatal("pooled resume returned a conversation after marker mismatch")
	}
}

// TestCommandRunner_ResumeInWorktree_FenceFailsClosedOnProbeError pins the
// documented best-effort arm: a live-instance probe that ERRORS (closed
// store standing in for any transient SQL failure) fences the pooled
// resume with the removed-instance text instead of passing through or
// panicking.
func TestCommandRunner_ResumeInWorktree_FenceFailsClosedOnProbeError(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
	store, mainDir, wtDir := fx.Store, fx.MainDir, fx.WorktreeDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}

	// Bind the pool's seed session itself (bare first, context after -
	// the REPL ordering), so the picker row resolves to a POOLED bound
	// entry and the click-time fence is the code that must answer.
	sess := chat.NewSession(res, &nullCompleter{})
	sess.SessionID = "session-main"
	if err := startInRouteErrOnly(context.Background(), sess, store, mainDir,
		worktreeroute.Route{Worktree: "wt1", Dir: wtDir}); err != nil {
		t.Fatalf("bind seed session: %v", err)
	}
	installCtx(t, sess, store, mainDir)
	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)
	t.Chdir(mainDir)

	store.Close() // wedge every SQL probe from here on

	out := runner.ResumeInWorktree(context.Background(),
		ports.SessionSummary{ID: "session-main", Worktree: "wt1", WorktreeDir: wtDir, WorktreeInstanceID: "wt_0001020304050607"})
	if out.Err == "" || !strings.Contains(out.Err, `worktree "wt1" was removed`) {
		t.Fatalf("probe error did not fence fail-closed: %q", out.Err)
	}
}

// TestCommandRunner_StartInWorktree_RefusesStaleInstanceRow arms
// StartInRoute's staleness check from the TUI: a picker row listed BEFORE
// the worktree was recreated carries the old instance id, and clicking it
// must be refused as stale - not silently bound to whatever instance is
// live now.
func TestCommandRunner_StartInWorktree_RefusesStaleInstanceRow(t *testing.T) {
	stubWorkflowWiring(t)
	store, _, mainDir, wtDir := worktreeCatalogFixture(t)
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	out := runner.StartInWorktree(context.Background(), ports.SessionSummary{
		ID: "worktree:wt1", Worktree: "wt1", WorktreeDir: wtDir, WorktreeRoute: true,
		WorktreeInstanceID: "wt_00000000000000dd", // not the live instance
	})
	if out.Err == "" || !strings.Contains(out.Err, "stale instance in request") {
		t.Fatalf("stale-instance row was not refused: %q", out.Err)
	}
}
