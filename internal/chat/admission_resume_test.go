package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// replayWidener stands in for the host's rebuild: it registers a fixed core
// tool plus every admitted name and publishes through TryPublishAgentSurface,
// so a test can inspect the live registry the session ends up advertising.
type replayWidener struct {
	mu       sync.Mutex
	calls    [][]string
	requests []AgentSurfacePublication
	sess     *Session
}

func (w *replayWidener) fn(admitted []string, req AgentSurfacePublication) (bool, error) {
	w.mu.Lock()
	w.calls = append(w.calls, slices.Clone(admitted))
	w.requests = append(w.requests, req)
	sess := w.sess
	w.mu.Unlock()
	registry := tools.NewRegistry()
	registry.Register(fixedBodyTool{name: "core_tool"})
	for _, name := range admitted {
		registry.Register(fixedBodyTool{name: name})
	}
	req.Registry = registry
	return sess.TryPublishAgentSurface(req), nil
}

func (w *replayWidener) snapshot() ([][]string, []AgentSurfacePublication) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls), slices.Clone(w.requests)
}

// seedSavedAdmission puts the session in the state a completed admission
// leaves behind - names admitted AND registered on the live surface - and
// writes a snapshot carrying the record.
func seedSavedAdmission(t *testing.T, sess *Session, name string, admitted ...string) {
	t.Helper()
	live := tools.NewRegistry()
	live.Register(fixedBodyTool{name: "core_tool"})
	for _, tool := range admitted {
		live.Register(fixedBodyTool{name: tool})
	}
	sess.mu.Lock()
	sess.Tools = live
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	admitTools(sess, admitted...)
	if err := sess.Save(name); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func liveSurfaceHas(sess *Session, name string) bool {
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.Tools == nil {
		return false
	}
	_, ok := sess.Tools.Get(name)
	return ok
}

// TestResumeRespectsTheSwitchGuard is R2-2 on the load path: background
// orchestration still owns the session dispatcher, so a resume must not
// republish (and thereby Close) it.
func TestResumeRespectsTheSwitchGuard(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named", "grep")

	var guardCalls int
	sess.SetSwitchGuard(func() error {
		guardCalls++
		return errors.New("a dispatch_tasks run is still active")
	})
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if guardCalls == 0 {
		t.Fatal("resume never consulted the switch guard")
	}
	if calls, _ := widener.snapshot(); len(calls) != 0 {
		t.Fatalf("resume republished the surface %d times while background work owned the dispatcher", len(calls))
	}
	// The guard refused BOTH republications, so grep is still live. The note
	// must say that, and the reported set must still contain it: claiming the
	// tool was dropped while it remains invocable is the divergence, not the
	// fix (see TestResumeThatCannotNarrowKeepsReportingTheLiveTools).
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "grep") || !strings.Contains(notes[0], "stay loaded") {
		t.Fatalf("notes = %v, want one note saying grep stays loaded", notes)
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep]: the refused narrowing left it live", got)
	}
}

// TestResumeRequiresTheSurfaceGenerationItRead pins the other missing D7
// precondition: the publication is valid only against the binding the record
// was read under.
func TestResumeRequiresTheSurfaceGenerationItRead(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named", "grep")
	sess.mu.RLock()
	generation := sess.agentSurfaceGeneration
	sess.mu.RUnlock()

	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	_, requests := widener.snapshot()
	if len(requests) != 1 {
		t.Fatalf("widener called %d times, want one replay publication", len(requests))
	}
	if requests[0].RequireSurfaceGeneration != generation {
		t.Fatalf("RequireSurfaceGeneration = %d, want %d", requests[0].RequireSurfaceGeneration, generation)
	}
	if requests[0].RequireSoleActiveTurn {
		t.Fatal("a load has no active turn; requiring one would make replay unpublishable")
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v after replay", got)
	}
}

// TestResumeWithAMismatchedBindingNarrowsTheLiveSurface: reporting the tools
// as dropped while still advertising them to the provider is the worst of both
// worlds - the next Save writes an empty record for a live tool.
func TestResumeWithAMismatchedBindingNarrowsTheLiveSurface(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named", "grep")

	sess.SetAdmissionBinding("reader", "digest-2")
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v, want the stale set dropped", got)
	}
	if liveSurfaceHas(sess, "grep") {
		t.Fatal("the dropped tool is still registered on the live surface")
	}
	if !liveSurfaceHas(sess, "core_tool") {
		t.Fatal("narrowing to core removed the core tier too")
	}
	calls, _ := widener.snapshot()
	if len(calls) != 1 || len(calls[0]) != 0 {
		t.Fatalf("widener calls = %v, want one core-only republication", calls)
	}
}

// TestResumeWithAnEmptyRecordNarrowsTheLiveSurface covers the path that emits
// no note: nothing was admitted in the snapshot, so nothing may stay loaded.
func TestResumeWithAnEmptyRecordNarrowsTheLiveSurface(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named")
	// The snapshot holds no record, but the live session has since admitted a
	// tool: resuming must put the surface back where the record says it is.
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Tools.Register(fixedBodyTool{name: "grep"})
	sess.mu.Unlock()

	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if liveSurfaceHas(sess, "grep") {
		t.Fatal("an empty record left a previously admitted tool registered")
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want silence: narrowing is not a surprise", notes)
	}
}

// TestOrdinaryResumeDoesNotChurnTheSurface: a load with nothing admitted
// before or after must not rebuild (and close) the dispatcher for nothing.
func TestOrdinaryResumeDoesNotChurnTheSurface(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named")
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if calls, _ := widener.snapshot(); len(calls) != 0 {
		t.Fatalf("widener called %d times for a load that changes nothing", len(calls))
	}
}

// --- pending-stage ownership -------------------------------------------

// TestAnUnrelatedTurnKeepsAnotherTurnsStage: a deferred stage legitimately
// outlives its staging turn, so a later turn's failure must not destroy it.
func TestAnUnrelatedTurnKeepsAnotherTurnsStage(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{err: errors.New("provider down")})
	contextSessionManager(t, sess, nil)
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		t.Error("an errored turn published an admission")
		return false, nil
	})
	// Owned by a turn other than the one about to run and fail.
	if _, err := sess.StageToolAdmission([]string{"grep"}, 7); err != nil {
		t.Fatalf("stage: %v", err)
	}
	stage, _ := sess.PendingAdmission()

	if _, err := sess.SendUser(context.Background(), "question", io.Discard); err == nil {
		t.Fatal("expected the provider error to surface")
	}
	got, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("an unrelated failing turn destroyed another turn's pending stage")
	}
	if got.TurnID != stage.TurnID || !slices.Equal(got.Names, []string{"grep"}) {
		t.Fatalf("stage = %+v, want the original turn's stage intact", got)
	}
}

// TestAppendingToAStageMovesItsOwnership: a stage folded into by a later turn
// belongs to that turn, so that turn's boundary owns publishing and dropping it.
func TestAppendingToAStageMovesItsOwnership(t *testing.T) {
	sess := newAdmissionSession(t)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 2); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	const want = 5
	if _, err := sess.StageToolAdmission([]string{"glob"}, want); err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage")
	}
	if stage.TurnID != want {
		t.Fatalf("stage.TurnID = %d, want %d (the turn that last touched it)", stage.TurnID, want)
	}
}

// --- attempt bound -------------------------------------------------------

func TestChargeAdmissionAttemptEnforcesThePerBindingBound(t *testing.T) {
	sess := newAdmissionSession(t)
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if err := sess.ChargeAdmissionAttempt(); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := sess.ChargeAdmissionAttempt(); err == nil {
		t.Fatalf("attempt %d was allowed past the bound of %d", tools.MaxAdmissionAttempts+1, tools.MaxAdmissionAttempts)
	}
}

// TestStageToolAdmissionNoLongerChargesTheAttemptBound: the host charges the
// attempt before argument parsing, so staging must not double-charge it.
func TestStageToolAdmissionNoLongerChargesTheAttemptBound(t *testing.T) {
	sess := newAdmissionSession(t)
	for i := 0; i < tools.MaxAdmissionAttempts+2; i++ {
		// Distinct names: a repeat would be a no-op, which has its own
		// (refunded, separately bounded) accounting.
		if _, err := sess.StageToolAdmission([]string{fmt.Sprintf("tool%d", i)}, 0); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
	}
	if err := sess.ChargeAdmissionAttempt(); err != nil {
		t.Fatalf("staging consumed the attempt bound: %v", err)
	}
}

// --- remainder spool -----------------------------------------------------

// TestRemainderSpoolPublishesUnderTheSessionLock is the -race proof: the host
// may republish the spool exactly while a turn is capturing its snapshot.
func TestRemainderSpoolPublishesUnderTheSessionLock(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			sess.SetRemainderSpool(remainder.NewSpool(nil))
		}
	}()
	for i := 0; i < 20; i++ {
		if _, err := sess.SendUser(context.Background(), "question", io.Discard); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	<-done
}

// TestResumeThatCannotNarrowKeepsReportingTheLiveTools: when the host refuses
// the core-only republication, the tools stay live. The session must then keep
// reporting - and persisting - them, because a state that claims LESS than the
// registry can invoke is the divergence narrowing exists to prevent.
func TestResumeThatCannotNarrowKeepsReportingTheLiveTools(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)
	sess.SetAdmissionBinding("reader", "digest-1")
	seedSavedAdmission(t, sess, "named", "grep")
	sess.SetSwitchGuard(func() error { return errors.New("a dispatch_tasks run is still active") })

	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !liveSurfaceHas(sess, "grep") {
		t.Fatal("precondition: the refused narrowing must leave grep live")
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want [grep]: the session may not report less than it can invoke", got)
	}
	if err := sess.Save("named"); err != nil {
		t.Fatalf("resave: %v", err)
	}
	record, err := sess.loadAdmission("named")
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if !slices.Equal(record.Names, []string{"grep"}) {
		t.Fatalf("persisted names = %v, want [grep] to match the live surface", record.Names)
	}
}

func TestTurnIDFromContextRejectsAnUnstampedCaller(t *testing.T) {
	// No caller frame at all: an out-of-band invocation owns no turn.
	if _, ok := TurnIDFromContext(context.Background()); ok {
		t.Fatal("a context with no caller frame reported a turn id")
	}
	// A caller frame whose turn id is not a "turn:N" stamp, and one whose
	// digits do not parse: both mean "no owning turn", never turn zero.
	for _, turnID := range []string{"", "session", "turn:", "turn:not-a-number", "turn:-1"} {
		ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "s", TurnID: turnID})
		if id, ok := TurnIDFromContext(ctx); ok {
			t.Fatalf("TurnID %q parsed as %d, want no owning turn", turnID, id)
		}
	}
}

func TestSurfaceAdvertisesAnyNeedsALiveRegistry(t *testing.T) {
	sess := newAdmissionSession(t)
	if sess.surfaceAdvertisesAny([]string{"grep"}) {
		t.Fatal("a session with no registry reported a live tool")
	}
	sess.Tools = tools.NewRegistry()
	if sess.surfaceAdvertisesAny([]string{"grep"}) {
		t.Fatal("an empty registry reported a live tool")
	}
}

// TestResumeWithAnEmptyRecordThatCannotNarrowKeepsReportingTheLiveTools covers
// the empty-record branch of the same fail-closed rule: the record admits
// nothing, but if the surface cannot be narrowed the session must keep
// reporting what is still callable.
func TestResumeWithAnEmptyRecordThatCannotNarrowKeepsReportingTheLiveTools(t *testing.T) {
	sess, _ := contextCatalogSession(t)
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(admissionProbeTool{name: "grep"})
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		return false, errors.New("rebuild failed")
	})
	sess.SetAdmissionBinding("reader", "digest-1")
	admitTools(sess, "grep")
	sess.mu.Lock()
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: "q"}, {Role: provider.RoleAssistant, Content: "a"}}
	sess.mu.Unlock()
	// Saved with nothing admitted, so the record is empty on resume.
	admitted := sess.AdmittedTools()
	admitTools(sess)
	if err := sess.Save("named"); err != nil {
		t.Fatalf("save: %v", err)
	}
	admitTools(sess, admitted...)
	if err := sess.Load("named"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want the still-live tool kept reported", got)
	}
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "grep") {
		t.Fatalf("notes = %v, want one note naming the retained tool", notes)
	}
}

// admissionProbeTool is a registry member for surface-content assertions.
type admissionProbeTool struct{ name string }

func (t admissionProbeTool) Name() string               { return t.name }
func (t admissionProbeTool) Description() string        { return t.name }
func (t admissionProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t admissionProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func TestCloseDispatcherClosesWhatIsLiveNow(t *testing.T) {
	sess := newAdmissionSession(t)
	// A session that never attached one has nothing to close and must not panic.
	sess.CloseDispatcher()
	(*Session)(nil).CloseDispatcher()

	first := newProbeDispatcher(t)
	sess.SetDispatcher(first.dispatcher)
	second := newProbeDispatcher(t)
	// Replacing the dispatcher closes the one it replaces.
	sess.SetDispatcher(second.dispatcher)
	if !first.closed() {
		t.Fatal("SetDispatcher left the replaced dispatcher open")
	}
	sess.CloseDispatcher()
	if !second.closed() {
		t.Fatal("CloseDispatcher did not close the live dispatcher")
	}
}

// TestCloseDispatcherFallsBackToTheCompatibilityField covers a session whose
// dispatcher was assigned to the public field without a binding.
func TestCloseDispatcherFallsBackToTheCompatibilityField(t *testing.T) {
	sess := newAdmissionSession(t)
	probe := newProbeDispatcher(t)
	sess.mu.Lock()
	sess.Dispatcher = probe.dispatcher
	sess.mu.Unlock()
	sess.CloseDispatcher()
	if !probe.closed() {
		t.Fatal("CloseDispatcher ignored the compatibility field")
	}
}

// probeDispatcher reports whether Close ran, via the OnClose hook.
type probeDispatcher struct {
	dispatcher *runtime.Dispatcher
	done       chan struct{}
}

func newProbeDispatcher(t *testing.T) *probeDispatcher {
	t.Helper()
	d, err := runtime.NewToolDispatcher(tools.NewRegistry(), runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	probe := &probeDispatcher{dispatcher: d, done: make(chan struct{})}
	d.OnClose(func() { close(probe.done) })
	return probe
}

func (p *probeDispatcher) closed() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
