package chat

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// admitTools puts the session into the state a completed admission leaves
// behind, without needing a real dispatcher rebuild.
func admitTools(sess *Session, names ...string) {
	sess.mu.Lock()
	sess.admittedTools = slices.Clone(names)
	sess.mu.Unlock()
}

func TestAdmissionPersistenceIsANoopWithNoStoreAtAll(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	if err := sess.persistAdmission("snap"); err != nil {
		t.Fatalf("persist with no store: %v", err)
	}
	got, err := sess.loadAdmission("snap")
	if err != nil || len(got.Names) != 0 {
		t.Fatalf("record = %+v, err = %v", got, err)
	}
}

func contextCatalogSession(t *testing.T) (*Session, *storage.SQLite) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}
	sess.SetBindingFactory(func(providerName, model string) (ModelBinding, error) {
		return ModelBinding{ProviderName: providerName, Model: model, Completer: &fakeCompleter{out: "answer"}}, nil
	})
	return sess, store
}

// TestContextCatalogReplaysTheAdmittedSet is the D3 durable path: save writes
// the record beside the transcript, and Load replays it through the host
// widener before the session can issue a request.
func TestContextCatalogReplaysTheAdmittedSet(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widened := make(chan []string, 4)
	sess.SetSurfaceWidener(func(admitted []string, req AgentSurfacePublication) (bool, error) {
		widened <- slices.Clone(admitted)
		return sess.TryPublishAgentSurface(req), nil
	})
	sess.SetAdmissionBinding("reader", "digest-1")
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	if err := sess.Save("named"); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess.ResetAdmissions()
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	select {
	case names := <-widened:
		if !slices.Equal(names, []string{"grep"}) {
			t.Fatalf("replayed %v, want [grep]", names)
		}
	default:
		t.Fatal("resume did not replay the admitted set")
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v after replay", got)
	}
}

func TestContextCatalogDropsTheSetWhenTheDigestChanged(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	// The only publication a mismatch may make is the narrowing one: the
	// dropped names must leave the live surface as well as the reported set.
	var widened [][]string
	sess.SetSurfaceWidener(func(admitted []string, _ AgentSurfacePublication) (bool, error) {
		widened = append(widened, slices.Clone(admitted))
		return true, nil
	})
	defer func() {
		if len(widened) != 1 || len(widened[0]) != 0 {
			t.Errorf("widener calls = %v, want one core-only republication", widened)
		}
	}()
	sess.SetAdmissionBinding("reader", "digest-1")
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	if err := sess.Save("named"); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess.SetAdmissionBinding("reader", "digest-2")
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want the stale set dropped", got)
	}
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestReplayWithoutAWidenerDropsTheSet(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	sess.SetAdmissionBinding("reader", "digest-1")
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	if err := sess.Save("named"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v with no host widener", got)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 1 {
		t.Fatalf("notes = %v, want one drop note", notes)
	}
}

func TestReplayReportsAFailedWiden(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		return false, errors.New("rebuild failed")
	})
	sess.SetAdmissionBinding("reader", "digest-1")
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	if err := sess.Save("named"); err != nil {
		t.Fatalf("save: %v", err)
	}
	sess.ResetAdmissions()
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after a failed replay", got)
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 1 {
		t.Fatalf("notes = %v, want one drop note", notes)
	}
}

// --- turn-boundary error paths -----------------------------------------

func agentTurnSession(t *testing.T, completer provider.Completer) *Session {
	t.Helper()
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, completer)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	return sess
}

// stageForNextTurn stages names and re-keys the stage to the turn that the
// next SendUser will run. In production load_tools executes inside that turn,
// so the stage carries its id; a test that stages beforehand would otherwise
// produce a stage no turn boundary owns.
func stageForNextTurn(t *testing.T, sess *Session, names ...string) {
	t.Helper()
	sess.mu.RLock()
	next := sess.turnID + 1
	sess.mu.RUnlock()
	if _, err := sess.StageToolAdmission(names, next); err != nil {
		t.Fatalf("stage: %v", err)
	}
}

// stageForCurrentTurn stages names owned by the turn the session is on right
// now, which is the turn a directly captured OperationToken carries.
func stageForCurrentTurn(t *testing.T, sess *Session, names ...string) {
	t.Helper()
	sess.mu.RLock()
	current := sess.turnID
	sess.mu.RUnlock()
	if _, err := sess.StageToolAdmission(names, current); err != nil {
		t.Fatalf("stage: %v", err)
	}
}

// TestErroredContextTurnPublishesItsStageOnceCommitted: an errored turn whose
// history still commits durably (finishErroredContextTurn's
// OutcomeUpstreamErr path) publishes its pending admission exactly like a
// successful turn does - if the history is durably committed, the admission
// decision made against that history is committed too. This matches the
// legacy (non-context) path's commitPreparedTurn, which already publishes on
// any successful persistence regardless of turnErr. This test previously
// asserted the opposite (admission must never publish on an errored turn),
// which reintroduced a cross-backend inconsistency the context path had
// already fixed once (commit 9fd44789) and then silently regressed.
func TestErroredContextTurnPublishesItsStageOnceCommitted(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{err: errors.New("provider down")})
	contextSessionManager(t, sess, nil)
	widened := false
	sess.SetSurfaceWidener(func(names []string, _ AgentSurfacePublication) (bool, error) {
		widened = true
		if len(names) != 1 || names[0] != "grep" {
			t.Fatalf("widened names = %v, want [grep]", names)
		}
		return true, nil
	})
	stageForNextTurn(t, sess, "grep")
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if !widened {
		t.Fatal("errored-but-committed turn did not publish its staged admission")
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a published stage must not still be pending")
	}
	if got := sess.AdmittedTools(); len(got) != 1 || got[0] != "grep" {
		t.Fatalf("admitted = %v, want [grep]", got)
	}
}

// TestCommitFailureDropsTheStage: publication happens only after the turn's
// history is durably committed, never on the failure branch.
func TestCommitFailureDropsTheStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	contextSessionManager(t, sess, errors.New("checkpoint failed"))
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("a failed commit published an admission")
		return false, nil
	})
	stageForNextTurn(t, sess, "grep")
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err == nil {
		t.Fatal("expected the checkpoint failure to surface")
	}
	// The durable commit never succeeded, so the staged admission must not
	// survive to a later turn's boundary (plan tools/05 D7: never publish in
	// the Commit-failure branch).
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a failed commit left its stage pending")
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after a failed commit", got)
	}
}

// TestCommittedContextTurnPublishesItsStage is the positive half: the durable
// path reaches the publication after adopting the new head.
func TestCommittedContextTurnPublishesItsStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	contextSessionManager(t, sess, nil)
	published := make(chan struct{}, 1)
	sess.SetSurfaceWidener(func(admitted []string, req AgentSurfacePublication) (bool, error) {
		if !slices.Equal(admitted, []string{"grep"}) {
			t.Errorf("admitted = %v", admitted)
		}
		published <- struct{}{}
		return sess.TryPublishAgentSurface(req), nil
	})
	stageForNextTurn(t, sess, "grep")
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	select {
	case <-published:
	default:
		t.Fatal("a committed turn did not publish its stage")
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v", got)
	}
}

// TestLegacyPersistFailureDropsTheStage: the legacy path publishes only after
// the turn's history is saved, so a save failure must drop the stage too.
func TestLegacyPersistFailureDropsTheStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("a failed save published an admission")
		return false, nil
	})
	stageForNextTurn(t, sess, "grep")
	sess.Completer = supersedeDuringTurn(sess)
	sess.mu.Lock()
	sess.binding.Completer = sess.Completer
	sess.mu.Unlock()
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a superseded legacy turn left its stage pending")
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v", got)
	}
}

// TestSupersededContextTurnDropsItsStage covers the stale-fence branch of the
// durable path (R2-1: a superseded turn never publishes).
func TestSupersededContextTurnDropsItsStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	prep, _ := contextSessionManager(t, sess, nil)
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("a superseded turn published an admission")
		return false, nil
	})
	stageForNextTurn(t, sess, "grep")
	sess.Completer = supersedeDuringTurn(sess)
	sess.mu.Lock()
	sess.binding.Completer = sess.Completer
	sess.mu.Unlock()
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if prep.discards == 0 {
		t.Fatal("a superseded turn kept its preparation")
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a superseded turn left its stage pending")
	}
}

// TestTurnWithoutAPreparationDropsItsStage covers the branch where the loop
// finished but never prepared a durable turn.
func TestTurnWithoutAPreparationDropsItsStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	loop := &agent.Loop{}
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("a turn with no preparation published an admission")
		return false, nil
	})
	// finishAgentTurn is driven directly here, with a token captured from the
	// turn the session is already on, so the stage must be owned by that turn.
	stageForCurrentTurn(t, sess, "grep")
	cfg := contextTurnConfig{manager: &contextmgr.ContextManager{
		PreparationManager: contextmgr.StructuralPreparationManager{}, Enabled: true,
	}}
	err := sess.finishAgentTurn(context.Background(), loop, nil, "q", "q",
		sess.captureOperationToken("t"), nil, cfg, nil)
	if !errors.Is(err, contextstate.ErrCheckpointConflict) {
		t.Fatalf("error = %v, want a checkpoint conflict", err)
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a turn with no preparation left its stage pending")
	}
}

// supersedeDuringTurn returns a completer that bumps the session turn id while
// the turn is still running, which is what a force-send does: the running
// turn's fence goes stale before it can commit.
func supersedeDuringTurn(sess *Session) provider.Completer {
	return &hookCompleter{out: "answer", before: func() {
		sess.mu.Lock()
		sess.turnID++
		sess.mu.Unlock()
	}}
}

type hookCompleter struct {
	out    string
	before func()
	fired  bool
}

func (h *hookCompleter) Name() string { return "hook" }

func (h *hookCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return h.ChatStream(ctx, req, io.Discard)
}

func (h *hookCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	h.fire()
	if w != nil {
		_, _ = io.WriteString(w, h.out)
	}
	return h.out, nil
}

func (h *hookCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	h.fire()
	return &provider.Response{Content: h.out, FinishReason: "stop"}, nil
}

func (h *hookCompleter) fire() {
	if h.fired || h.before == nil {
		return
	}
	h.fired = true
	h.before()
}

// TestErroredTurnWithAPreparationDiscardsIt covers the branch where the loop
// did prepare a durable turn but the turn itself failed.
func TestErroredTurnWithAPreparationDiscardsIt(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	prep, _ := contextSessionManager(t, sess, nil)
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("an errored turn published an admission")
		return false, nil
	})
	// Driven through finishAgentTurn directly: the token carries the current turn.
	stageForCurrentTurn(t, sess, "grep")
	loop := &agent.Loop{HasPreparation: true}
	cfg := sess.captureContextForTest()
	before := prep.discards
	err := sess.finishAgentTurn(context.Background(), loop, nil, "q", "q",
		sess.captureOperationToken("t"), nil, cfg, errors.New("tool blew up"))
	if err != nil {
		t.Fatalf("an errored turn must not report a second failure: %v", err)
	}
	if prep.discards != before+1 {
		t.Fatalf("discards = %d, want the preparation released", prep.discards)
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("an errored turn left its stage pending")
	}
}

// failingCatalog is a context store whose session catalog rejects writes.
type failingCatalog struct {
	contextstate.Store
	contextstate.SessionCatalog
	err error
}

func (f failingCatalog) SaveSession(context.Context, contextstate.Principal, string, []byte, string, string, int, int, int, contextstate.SessionSaveOptions) error {
	return f.err
}

func TestContextCatalogSaveFailurePropagates(t *testing.T) {
	sess, store := contextCatalogSession(t)
	want := errors.New("catalog rejected")
	sess.mu.Lock()
	sess.contextStore = failingCatalog{Store: store, SessionCatalog: store, err: want}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}}
	sess.mu.Unlock()
	if err := sess.Save("named"); !errors.Is(err, want) {
		t.Fatalf("error = %v, want the catalog failure", err)
	}
}

// driftingPublisher makes the in-memory fence go stale between the durable
// commit and its adoption, which is the resync branch: the turn landed on
// disk but the session moved on underneath it.
type driftingPublisher struct {
	sess *Session
}

func (p driftingPublisher) Commit(context.Context, contextmgr.Preparation, contextmgr.TurnResult) error {
	p.sess.mu.Lock()
	p.sess.operationEpoch++
	p.sess.mu.Unlock()
	return nil
}

func TestFenceDriftAfterCommitResyncsInsteadOfWedging(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}
	manager := &contextmgr.ContextManager{
		PreparationManager:  &contextPreparationProbe{},
		CheckpointPublisher: driftingPublisher{sess: sess},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("a drifted fence published an admission")
		return false, nil
	})
	stageForNextTurn(t, sess, "grep")
	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after a drifted fence", got)
	}
}
