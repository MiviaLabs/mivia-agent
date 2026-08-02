package chat

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
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
	if _, err := sess.StageToolAdmission([]string{"grep", "glob"}); err != nil {
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
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	if _, err := sess.StageToolAdmission([]string{"glob"}); err != nil {
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
		if _, err := sess.StageToolAdmission([]string{fmt.Sprintf("tool%d", i)}); err != nil {
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
	if _, err := sess.StageToolAdmission([]string{"one-too-many"}); err == nil {
		t.Fatalf("publication %d was allowed past the bound of %d", tools.MaxAdmissionPublications+1, tools.MaxAdmissionPublications)
	}
}

// TestSiblingTurnBlocksPublication is R2-1: with a second turn in flight, the
// old dispatcher may still be executing, so nothing may close it.
func TestSiblingTurnBlocksPublication(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
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
func TestTryPublishAgentSurfaceRechecksAtomically(t *testing.T) {
	sess := newAdmissionSession(t)
	sess.mu.Lock()
	sess.activeTurns = 1
	sess.turnID = 7
	generation := sess.agentSurfaceGeneration
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

// TestStageSurvivesAModelSwitch is R2-5: a model switch preserves the agent
// surface generation, so the stage authored before it is still valid.
func TestStageSurvivesAModelSwitch(t *testing.T) {
	sess := newAdmissionSession(t)
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
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
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
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
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
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
	if _, err := sess.StageToolAdmission([]string{"grep"}); err != nil {
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
		if _, err := sess.StageToolAdmission(nil); err != nil {
			t.Fatalf("attempt %d after reset: %v", i, err)
		}
	}
}
