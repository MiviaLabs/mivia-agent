package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// recordingWidener captures what the session asked to publish and lets a test
// decide the outcome, standing in for the host's dispatcher rebuild.
type recordingWidener struct {
	mu       sync.Mutex
	calls    [][]string
	requests []AgentSurfacePublication
	publish  func(AgentSurfacePublication) (bool, error)
}

func (w *recordingWidener) fn(admitted []string, req AgentSurfacePublication) (bool, error) {
	w.mu.Lock()
	w.calls = append(w.calls, slices.Clone(admitted))
	w.requests = append(w.requests, req)
	publish := w.publish
	w.mu.Unlock()
	if publish != nil {
		return publish(req)
	}
	return true, nil
}

func (w *recordingWidener) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

func newAdmissionSession(t *testing.T) *Session {
	t.Helper()
	return NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, nil)
}

func TestStageToolAdmissionCapturesTheCurrentBinding(t *testing.T) {
	sess := newAdmissionSession(t)
	if _, err := sess.StageToolAdmission([]string{"grep", "glob"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage")
	}
	if !slices.Equal(stage.Names, []string{"grep", "glob"}) {
		t.Fatalf("stage names = %v", stage.Names)
	}
	if stage.SurfaceGeneration == 0 {
		t.Fatal("stage did not capture the agent surface generation")
	}
}

func TestStageToolAdmissionBatchesIntoOnePublication(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"glob"}, 0); err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if widener.count() != 1 {
		t.Fatalf("widener called %d times, want one publication for the whole batch", widener.count())
	}
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep", "glob"}) {
		t.Fatalf("admitted = %v, want both staged names", got)
	}
}

func TestPublicationBoundIsChargedPerBatchNotPerName(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	for i := 0; i < tools.MaxAdmissionPublications; i++ {
		if _, err := sess.StageToolAdmission([]string{fmt.Sprintf("tool%d", i)}, 0); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
		sess.mu.Lock()
		sess.activeTurns = 1
		sess.mu.Unlock()
		sess.PublishPendingAdmission()
		sess.mu.Lock()
		sess.activeTurns = 0
		sess.mu.Unlock()
	}
	if _, err := sess.StageToolAdmission([]string{"one-too-many"}, 0); err == nil {
		t.Fatalf("publication %d was allowed past the bound of %d", tools.MaxAdmissionPublications+1, tools.MaxAdmissionPublications)
	}
}

// TestSiblingTurnBlocksPublication is R2-1: with a second turn in flight, the
// old dispatcher may still be executing, so nothing may close it.
func TestSiblingTurnBlocksPublication(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 2
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if widener.count() != 0 {
		t.Fatal("published while a sibling turn was active")
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending, not be dropped")
	}

	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want publication once the sibling finished", got)
	}
}

// TestTryPublishAgentSurfaceRechecksAtomically proves the precondition and the
// swap happen under one lock: a request whose stated turn is no longer current
// is refused even though the caller checked before building the candidate.
//
// Every precondition is re-checked here, not only by the caller. The caller's
// own check happens before the candidate surface is built, and a sibling turn
// starting in that window is precisely the R2-1 hazard: its dispatcher would be
// closed underneath it.
func TestTryPublishAgentSurfaceRechecksAtomically(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.mu.Lock()
	sess.activeTurns = 2
	sess.turnID = 7
	generation := sess.agentSurfaceGeneration
	sess.mu.Unlock()

	if sess.TryPublishAgentSurface(AgentSurfacePublication{
		RequireTurnID: 7, RequireSurfaceGeneration: generation, RequireSoleActiveTurn: true,
	}) {
		t.Fatal("published with a sibling turn still executing on the old dispatcher")
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()

	if sess.TryPublishAgentSurface(AgentSurfacePublication{
		RequireTurnID: 6, RequireSurfaceGeneration: generation, RequireSoleActiveTurn: true,
	}) {
		t.Fatal("published against a superseded turn")
	}
	if sess.TryPublishAgentSurface(AgentSurfacePublication{
		RequireTurnID: 7, RequireSurfaceGeneration: generation + 1, RequireSoleActiveTurn: true,
	}) {
		t.Fatal("published against an outdated surface generation")
	}
	if !sess.TryPublishAgentSurface(AgentSurfacePublication{
		Prompt: "P", RequireTurnID: 7, RequireSurfaceGeneration: generation, RequireSoleActiveTurn: true,
	}) {
		t.Fatal("refused a publication whose preconditions all hold")
	}
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.agentSurfaceGeneration != generation+1 {
		t.Fatalf("generation = %d, want %d after publication", sess.agentSurfaceGeneration, generation+1)
	}
}

// TestSurfaceSwitchBlocksPublication is the /agent-switch twin of
// TestSiblingTurnBlocksPublication (R2-1): while a switch holds the session it
// is building a whole new surface, and a publication landing in the middle
// would swap - and Close - the dispatcher out from under it.
func TestSurfaceSwitchBlocksPublication(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	live := newProbeDispatcher(t)
	sess.SetDispatcher(live.dispatcher)
	sess.mu.RLock()
	generation := sess.agentSurfaceGeneration
	sess.mu.RUnlock()

	release, err := sess.BeginSurfaceSwitch()
	if err != nil {
		t.Fatalf("begin switch: %v", err)
	}
	sess.PublishPendingAdmission()
	if widener.count() != 0 {
		t.Fatal("a staged admission was published while an /agent switch held the session")
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending across a switch, not be dropped")
	}
	// The load-shaped request is the one only the switch guard can refuse: an
	// /agent switch holds the session with no turn active, so no turn-count
	// precondition stands in for the check.
	if sess.TryPublishAgentSurface(AgentSurfacePublication{RequireSurfaceGeneration: generation}) {
		t.Fatal("published a surface while an /agent switch held the session")
	}
	if live.closed() {
		t.Fatal("a refused publication closed the dispatcher the switch still owns")
	}

	release()
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want publication once the switch released", got)
	}
}

// TestPublicationResetsTheDeferralNoteBudget: the bounded "still not loaded"
// note budget is per publication, not per binding lifetime. Without the reset
// one long session that once deferred twice never explains a deferral again -
// every later load_tools call silently does nothing from the user's side.
func TestPublicationResetsTheDeferralNoteBudget(t *testing.T) {
	sess := newAdmissionSession(t)
	refuse := true
	sess.SetSurfaceWidener(func([]string, AgentSurfacePublication) (bool, error) {
		return !refuse, nil
	})
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()

	for i := 0; i < maxAdmissionDeferralNotes; i++ {
		sess.PublishPendingAdmission()
	}
	if notes := sess.TakeAdmissionNotes(); len(notes) != maxAdmissionDeferralNotes {
		t.Fatalf("notes = %v, want one per deferral up to the bound of %d", notes, maxAdmissionDeferralNotes)
	}
	sess.PublishPendingAdmission()
	if notes := sess.TakeAdmissionNotes(); len(notes) != 0 {
		t.Fatalf("notes = %v, want silence past the bound", notes)
	}

	refuse = false
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want the retry to succeed", got)
	}

	refuse = true
	if _, err := sess.StageToolAdmission([]string{"glob"}, 0); err != nil {
		t.Fatalf("stage after publication: %v", err)
	}
	sess.PublishPendingAdmission()
	notes := sess.TakeAdmissionNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "glob") {
		t.Fatalf("notes = %v, want a fresh note naming glob: the budget is per publication", notes)
	}
}

// TestPublicationIsFencedToTheTurnItWasBuiltFor (R2-1): the host builds the
// candidate surface with the session lock released, and a force-sent turn can
// begin in that window. Publishing then would close the dispatcher the new turn
// is already running on, so the request must name the turn it was built for and
// the swap must refuse once that turn is superseded.
func TestPublicationIsFencedToTheTurnItWasBuiltFor(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.mu.Lock()
	sess.turnID = 4
	sess.activeTurns = 1
	sess.mu.Unlock()
	if _, err := sess.StageToolAdmission([]string{"grep"}, 4); err != nil {
		t.Fatalf("stage: %v", err)
	}
	var built int
	sess.SetSurfaceWidener(func(_ []string, req AgentSurfacePublication) (bool, error) {
		built++
		// A force-sent turn supersedes the one whose boundary is publishing,
		// exactly while the candidate is being assembled.
		sess.mu.Lock()
		sess.turnID++
		sess.mu.Unlock()
		return sess.TryPublishAgentSurface(req), nil
	})
	sess.PublishPendingAdmission()
	if built != 1 {
		t.Fatalf("the widener ran %d times, want the one boundary publication", built)
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v: published onto a turn that had already been superseded", got)
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending for the superseding turn's own boundary")
	}
}

// TestPublicationWithNoActiveTurnIsDeferred is the zero-turn half of the
// sole-active-turn rule: the boundary that publishes is the finishing turn's
// own. With no turn active there is no such boundary, and the host must not
// even be asked to build a candidate surface.
func TestPublicationWithNoActiveTurnIsDeferred(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.PublishPendingAdmission()
	if widener.count() != 0 {
		t.Fatal("published with no active turn")
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("the stage must stay pending, not be dropped")
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v with no turn active", got)
	}
}

// TestStageSurvivesAModelSwitch is R2-5: a model switch preserves the agent
// surface generation, so the stage authored before it is still valid.
func TestStageSurvivesAModelSwitch(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// A model switch bumps the model generation, never the surface generation.
	sess.mu.Lock()
	sess.binding.ModelGeneration++
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); !slices.Equal(got, []string{"grep"}) {
		t.Fatalf("admitted = %v, want the stage to survive a model switch", got)
	}
}

func TestStageDiesOnAnAgentSurfaceChange(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.agentSurfaceGeneration++ // what an /agent switch does
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if widener.count() != 0 {
		t.Fatal("a stage from a replaced binding was published")
	}
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("a stage from a replaced binding must be dropped, not left pending")
	}
}

func TestWidenerFailureLeavesTheStagePending(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{publish: func(AgentSurfacePublication) (bool, error) {
		return false, fmt.Errorf("rebuild failed")
	}}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.mu.Unlock()
	sess.PublishPendingAdmission()
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after a failed rebuild", got)
	}
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("a failed rebuild must leave the stage pending for a retry")
	}
}

func TestResetAdmissionsClearsEveryBudget(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.SetSurfaceWidener((&recordingWidener{}).fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("stage: %v", err)
	}
	sess.ResetAdmissions()
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("reset left a pending stage")
	}
	if got := sess.AdmittedTools(); len(got) != 0 {
		t.Fatalf("admitted = %v after reset", got)
	}
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if _, err := sess.StageToolAdmission(nil, 0); err != nil {
			t.Fatalf("attempt %d after reset: %v", i, err)
		}
	}
}

// TestNoOpStagingRefundsTheAttemptBudget: the frozen index keeps advertising a
// loaded tool as loadable, so an idempotent re-request is a mistake the design
// invites. It must not consume the budget a genuine request needs.
func TestNoOpStagingRefundsTheAttemptBudget(t *testing.T) {
	sess := newAdmissionSession(t)
	admitTools(sess, "grep")
	if err := sess.ChargeAdmissionAttempt(); err != nil {
		t.Fatalf("charge: %v", err)
	}
	result, err := sess.StageToolAdmission([]string{"grep"}, 0)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(result.Staged) != 0 || !slices.Equal(result.Already, []string{"grep"}) {
		t.Fatalf("stage = %+v, want a pure no-op", result)
	}
	for i := 0; i < tools.MaxAdmissionAttempts; i++ {
		if err := sess.ChargeAdmissionAttempt(); err != nil {
			t.Fatalf("a refunded no-op ate the budget: attempt %d: %v", i, err)
		}
	}
}

// TestConsecutiveNoOpStagingIsBounded: the refund is what makes a bound
// necessary - without one a model could re-request loaded tools forever for
// free, which is exactly what charging every attempt protects against.
func TestConsecutiveNoOpStagingIsBounded(t *testing.T) {
	sess := newAdmissionSession(t)
	admitTools(sess, "grep")
	for i := 0; i < maxConsecutiveAdmissionNoOps; i++ {
		if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
			t.Fatalf("no-op %d: %v", i, err)
		}
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err == nil {
		t.Fatalf("no-op %d was allowed past the bound of %d", maxConsecutiveAdmissionNoOps+1, maxConsecutiveAdmissionNoOps)
	}
	// A genuine request clears the streak: the bound is against a loop, not
	// against a model that occasionally re-asks for something it has.
	if _, err := sess.StageToolAdmission([]string{"glob"}, 0); err != nil {
		t.Fatalf("a genuine request after the bound: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("the streak was not reset by a genuine request: %v", err)
	}
}

// TestStagedThisTurnIsNotReportedAsAlreadyLoaded (R3): a name staged during
// this turn is NOT callable yet - publication happens at the turn boundary
// (D6). Folding it into the same bucket as a published admission makes the
// result tell the model to call a tool that does not exist yet.
func TestStagedThisTurnIsNotReportedAsAlreadyLoaded(t *testing.T) {
	sess := newAdmissionSession(t)
	admitTools(sess, "grep")
	if _, err := sess.StageToolAdmission([]string{"glob"}, 1); err != nil {
		t.Fatalf("stage: %v", err)
	}
	result, err := sess.StageToolAdmission([]string{"grep", "glob"}, 1)
	if err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if len(result.Staged) != 0 {
		t.Fatalf("staged = %v, want nothing new", result.Staged)
	}
	if !slices.Equal(result.Already, []string{"grep"}) {
		t.Fatalf("already = %v, want only the published grep", result.Already)
	}
	if !slices.Equal(result.AlreadyStaged, []string{"glob"}) {
		t.Fatalf("alreadyStaged = %v, want the staged-but-unpublished glob", result.AlreadyStaged)
	}
}

// TestNoOpStreakErrorDoesNotClaimStagedToolsAreCallable (R4): the streak error
// is the corrective message the refund design leans on. If it tells the model
// a staged-but-unpublished tool is callable now, the model calls it and gets
// unknown-tool.
func TestNoOpStreakErrorDoesNotClaimStagedToolsAreCallable(t *testing.T) {
	sess := newAdmissionSession(t)
	admitTools(sess, "grep")
	if _, err := sess.StageToolAdmission([]string{"glob"}, 1); err != nil {
		t.Fatalf("stage: %v", err)
	}
	var err error
	for i := 0; i <= maxConsecutiveAdmissionNoOps; i++ {
		_, err = sess.StageToolAdmission([]string{"grep", "glob"}, 1)
	}
	if err == nil {
		t.Fatal("the no-op streak bound never fired")
	}
	msg := err.Error()
	callable, _, ok := strings.Cut(msg, "callable now")
	if !ok {
		t.Fatalf("streak error says nothing about what is callable now: %q", msg)
	}
	if strings.Contains(callable, "glob") {
		t.Fatalf("streak error claims the staged-but-unpublished glob is callable now: %q", msg)
	}
	if !strings.Contains(msg, "glob") || !strings.Contains(msg, "next turn") {
		t.Fatalf("streak error gives no next-turn signal for the staged glob: %q", msg)
	}
}

// TestTotalLoadToolsCallsAreBoundedDespiteRefunds (R6): the refund must not
// turn MaxAdmissionAttempts into a much larger number. Capping refunds per
// binding keeps the real ceiling within a few calls of the stated bound.
func TestTotalLoadToolsCallsAreBoundedDespiteRefunds(t *testing.T) {
	sess := newAdmissionSession(t)
	admitTools(sess, "grep")
	calls := 0
	for calls < 1000 {
		if err := sess.ChargeAdmissionAttempt(); err != nil {
			break
		}
		calls++
		if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err == nil {
			continue
		}
		// The streak bound fired; the next call is a genuine one that clears it.
		if err := sess.ChargeAdmissionAttempt(); err != nil {
			break
		}
		calls++
		if _, err := sess.StageToolAdmission([]string{fmt.Sprintf("t%d", calls)}, 0); err != nil {
			t.Fatalf("genuine stage at call %d: %v", calls, err)
		}
	}
	ceiling := tools.MaxAdmissionAttempts + maxConsecutiveAdmissionNoOps + 1
	if calls > ceiling {
		t.Fatalf("a refunded no-op loop bought %d load_tools calls, want at most %d", calls, ceiling)
	}
}

// TestNoOpStreakResetsAtTheTurnBoundary (R7): the counter is documented as
// CONSECUTIVE, but nothing cleared it at a turn boundary, so one innocent
// re-request per turn hard-errored after a few turns.
func TestNoOpStreakResetsAtTheTurnBoundary(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "answer"})
	admitTools(sess, "grep")
	for i := 0; i < maxConsecutiveAdmissionNoOps; i++ {
		if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
			t.Fatalf("no-op %d: %v", i, err)
		}
	}
	if err := sess.finishAgentTurn(context.Background(), &agent.Loop{}, nil, "q", "q",
		sess.captureOperationToken("t"), nil, contextTurnConfig{}, nil); err != nil {
		t.Fatalf("turn boundary: %v", err)
	}
	for i := 0; i < maxConsecutiveAdmissionNoOps; i++ {
		if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
			t.Fatalf("the no-op streak survived a turn boundary: no-op %d: %v", i, err)
		}
	}
}

// --- stage ownership (M4) ------------------------------------------------

// TestDroppingOneTurnsStageKeepsAnotherTurnsNames (R5): a stage that outlives
// its staging turn (a deferral) is folded into by the next turn. Ownership must
// be per name, or the later turn's failure destroys the retry the earlier turn
// was promised.
func TestDroppingOneTurnsStageKeepsAnotherTurnsNames(t *testing.T) {
	sess := newAdmissionSession(t)
	if _, err := sess.StageToolAdmission([]string{"a"}, 1); err != nil {
		t.Fatalf("stage turn 1: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"b"}, 2); err != nil {
		t.Fatalf("stage turn 2: %v", err)
	}
	sess.dropPendingAdmissionForTurn(2)
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("turn 2's failure destroyed turn 1's still-pending stage")
	}
	if !slices.Equal(stage.Names, []string{"a"}) {
		t.Fatalf("stage names = %v, want only turn 1's name to survive", stage.Names)
	}
	sess.dropPendingAdmissionForTurn(1)
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("turn 1's own boundary did not drop the last of its names")
	}
}

// stageOwners is the owner set recorded for one staged name.
func stageOwners(t *testing.T, stage AdmissionStage, name string) map[uint64]struct{} {
	t.Helper()
	i := slices.Index(stage.Names, name)
	if i < 0 {
		t.Fatalf("%q is not staged; stage names = %v", name, stage.Names)
	}
	return stage.nameOwners[i]
}

// TestReRequestingAStagedNameAddsTheAskingTurnAsAnOwner (R4-2): a re-request is
// answered "already staged and callable from your next turn". The asking turn
// must therefore own the name too, or the original owner's boundary destroys
// the stage the asking turn was just promised.
func TestReRequestingAStagedNameAddsTheAskingTurnAsAnOwner(t *testing.T) {
	sess := newAdmissionSession(t)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 5); err != nil {
		t.Fatalf("stage turn 5: %v", err)
	}
	result, err := sess.StageToolAdmission([]string{"grep"}, 6)
	if err != nil {
		t.Fatalf("re-request turn 6: %v", err)
	}
	if !slices.Equal(result.AlreadyStaged, []string{"grep"}) {
		t.Fatalf("AlreadyStaged = %v, want the promise turn 6 was given", result.AlreadyStaged)
	}
	sess.dropPendingAdmissionForTurn(5)
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("turn 5's boundary destroyed the stage turn 6 was promised")
	}
	if !slices.Equal(stage.Names, []string{"grep"}) {
		t.Fatalf("stage names = %v, want grep to survive for turn 6", stage.Names)
	}
	if owners := stageOwners(t, stage, "grep"); len(owners) != 1 {
		t.Fatalf("owners of grep = %v, want turn 6 alone", owners)
	}
	sess.dropPendingAdmissionForTurn(6)
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("turn 6's own boundary did not drop the last owner of the name")
	}
}

// TestAnOutOfBandReRequestDoesNotPinAnOwnedStage: turn 0 means "no owning
// turn", and no boundary ever drops it. Recording it as a co-owner would make
// the stage immortal - every real owner's boundary leaves turn 0 behind.
func TestAnOutOfBandReRequestDoesNotPinAnOwnedStage(t *testing.T) {
	sess := newAdmissionSession(t)
	if _, err := sess.StageToolAdmission([]string{"grep"}, 5); err != nil {
		t.Fatalf("stage turn 5: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 0); err != nil {
		t.Fatalf("out-of-band re-request: %v", err)
	}
	sess.dropPendingAdmissionForTurn(5)
	if stage, ok := sess.PendingAdmission(); ok {
		t.Fatalf("an out-of-band re-request pinned turn 5's stage: %v", stage.Names)
	}
}

// TestStageIsOwnedByTheExecutingTurn: under force-send the session's current
// turn id belongs to the turn that SUPERSEDED the one running load_tools, so
// stamping the stage with it hands ownership to the wrong turn - the drop then
// destroys an innocent stage and the wrong boundary publishes.
func TestStageIsOwnedByTheExecutingTurn(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.mu.Lock()
	sess.turnID = 3 // a force-sent turn B has already begun
	sess.mu.Unlock()

	// ...while turn A, cancelled but still running its tool batch, stages.
	if _, err := sess.StageToolAdmission([]string{"grep"}, 2); err != nil {
		t.Fatalf("stage: %v", err)
	}
	stage, ok := sess.PendingAdmission()
	if !ok {
		t.Fatal("no pending stage")
	}
	owners := stageOwners(t, stage, "grep")
	if _, ok := owners[2]; !ok || len(owners) != 1 {
		t.Fatalf("owners of grep = %v, want turn 2 alone (the turn that executed load_tools)", owners)
	}
	sess.dropPendingAdmissionForTurn(3)
	if _, ok := sess.PendingAdmission(); !ok {
		t.Fatal("turn B's boundary destroyed turn A's stage")
	}
	sess.dropPendingAdmissionForTurn(2)
	if _, ok := sess.PendingAdmission(); ok {
		t.Fatal("turn A's own boundary did not drop its stage")
	}
}

// TestTurnIDFromContextReadsTheDispatcherCallerFrame pins the coupling the fix
// depends on: the id the host passes to StageToolAdmission comes from the
// dispatcher's caller frame, and it must be the id of the turn that is really
// executing the call.
func TestTurnIDFromContextReadsTheDispatcherCallerFrame(t *testing.T) {
	sess := agentTurnSession(t, &turnProbeCompleter{})
	var seen uint64
	var found bool
	sess.Tools.Register(turnProbeTool{record: func(ctx context.Context) {
		seen, found = TurnIDFromContext(ctx)
	}})

	if _, err := sess.SendUser(context.Background(), "go", io.Discard); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !found {
		t.Fatal("a tool executing inside a turn could not read its turn id")
	}
	sess.mu.RLock()
	want := sess.turnID
	sess.mu.RUnlock()
	if seen != want {
		t.Fatalf("turn id from the caller frame = %d, want the session's turn %d", seen, want)
	}
}

// turnProbeCompleter calls the probe tool once, then finishes.
type turnProbeCompleter struct{ calls int }

func (c *turnProbeCompleter) Name() string { return "turn-probe" }
func (c *turnProbeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
func (c *turnProbeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *turnProbeCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.calls++
	if c.calls == 1 {
		return toolCallResponse("tc-turn-probe", "turn_probe"), nil
	}
	return &provider.Response{Content: "done", FinishReason: "stop"}, nil
}

type turnProbeTool struct{ record func(context.Context) }

func (turnProbeTool) Name() string               { return "turn_probe" }
func (turnProbeTool) Description() string        { return "records its caller frame" }
func (turnProbeTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (turnProbeTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionRead}
}
func (t turnProbeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.record(ctx)
	return "ok", nil
}

// TestDuplicateNamesInOneCallStageOnce: a model that repeats a name inside a
// single request must not stage it twice, and the repeat is reported as staged
// rather than as loaded - it is not callable until the boundary either.
func TestDuplicateNamesInOneCallStageOnce(t *testing.T) {
	sess := newAdmissionSession(t)
	result, err := sess.StageToolAdmission([]string{"grep", "grep", "grep"}, 1)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !slices.Equal(result.Staged, []string{"grep"}) {
		t.Fatalf("staged = %v, want the name staged exactly once", result.Staged)
	}
	if !slices.Equal(result.AlreadyStaged, []string{"grep", "grep"}) {
		t.Fatalf("already staged = %v, want the repeats reported as staged", result.AlreadyStaged)
	}
	if len(result.Already) != 0 {
		t.Fatalf("already loaded = %v, want none - nothing has published", result.Already)
	}
	stage, ok := sess.PendingAdmission()
	if !ok || !slices.Equal(stage.Names, []string{"grep"}) {
		t.Fatalf("stage = %+v, want one entry", stage)
	}
	if owners := stageOwners(t, stage, "grep"); len(owners) != 1 {
		t.Fatalf("owners = %v, want the staging turn recorded once", owners)
	}
}
