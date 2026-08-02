package chat

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// maxAdmissionDeferralNotes bounds how many "still not loaded" notes one agent
// binding may surface. A stage that keeps missing its publication boundary is
// worth saying once, not once per turn.
const maxAdmissionDeferralNotes = 2

// AdmissionStage is one turn's recorded intent to widen the tool surface.
// load_tools executes inside a turn and cannot rebuild the surface it is
// running on (plan tools/05 D6/F2), so it records intent here and the turn
// boundary performs the publication.
type AdmissionStage struct {
	// Names are the deferred tools to admit, in the order they were staged.
	Names []string
	// SurfaceGeneration is the agent-surface generation captured at staging.
	// A stage whose generation no longer matches is dropped: it was authored
	// against a binding that an /agent switch has since replaced. A model
	// switch preserves the generation, so a stage survives one.
	SurfaceGeneration uint64
	// TurnID is the staging turn. Only that turn may publish the stage, so a
	// superseded or force-sent sibling turn never publishes someone else's
	// admission.
	TurnID uint64
}

// AgentSurfacePublication is a fully built candidate agent surface plus the
// preconditions that must still hold when it is published. Zero-valued
// requirements are not checked.
type AgentSurfacePublication struct {
	Prompt        string
	MaxSteps      int
	Registry      *tools.Registry
	Dispatcher    *runtime.Dispatcher
	SkillRegistry *skills.Registry
	// RequireTurnID publishes only while this turn is still the current one.
	RequireTurnID uint64
	// RequireSurfaceGeneration publishes only against the generation the
	// candidate was derived from.
	RequireSurfaceGeneration uint64
	// RequireSoleActiveTurn publishes only when exactly one turn is active -
	// the finishing turn that staged the admission. It is what makes closing
	// the previous dispatcher safe: no sibling turn and no background run can
	// still be executing on it.
	RequireSoleActiveTurn bool
}

// SurfaceWidener rebuilds the root agent surface with admitted appended to the
// core tier and publishes it through TryPublishAgentSurface. It reports whether
// the publication happened; false with a nil error means the preconditions were
// not met and the stage must stay pending. The host owns this callback because
// internal/chat cannot construct a session dispatcher.
type SurfaceWidener func(admitted []string, req AgentSurfacePublication) (bool, error)

// SetSurfaceWidener installs the host-owned admission publisher. Without one,
// staged admissions are dropped rather than silently accumulating.
func (s *Session) SetSurfaceWidener(widener SurfaceWidener) {
	s.mu.Lock()
	s.surfaceWidener = widener
	s.mu.Unlock()
}

// AdmittedTools returns the tools admitted into the current agent binding's
// surface, in admission order.
func (s *Session) AdmittedTools() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.admittedTools)
}

// PendingAdmission returns a copy of the stage awaiting publication, if any.
func (s *Session) PendingAdmission() (AdmissionStage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pendingAdmission == nil {
		return AdmissionStage{}, false
	}
	stage := *s.pendingAdmission
	stage.Names = slices.Clone(stage.Names)
	return stage, true
}

// AdmissionStageResult describes what one load_tools call did.
type AdmissionStageResult struct {
	// Staged are names newly recorded for admission at the next boundary.
	Staged []string
	// Already are names that are already loaded or already staged this turn.
	// They are free: they consume no publication budget.
	Already []string
}

// ChargeAdmissionAttempt charges the per-binding attempt bound and reports
// exhaustion. Returns nil when the call may proceed.
//
// It is separate from StageToolAdmission so the host can charge it before
// argument parsing: a model looping on unknown tool names never reaches
// staging, and would otherwise burn no budget at all.
func (s *Session) ChargeAdmissionAttempt() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admissionAttempts++
	if s.admissionAttempts > tools.MaxAdmissionAttempts {
		return fmt.Errorf("tool loading is exhausted for this agent: %d attempts already made (limit %d)", s.admissionAttempts-1, tools.MaxAdmissionAttempts)
	}
	return nil
}

// StageToolAdmission records intent to admit names into the tool surface.
//
// It charges the publication bound only when the call actually stages
// something new (plan tools/05 F7), so an idempotent re-request is free. The
// attempt bound is charged by the caller via ChargeAdmissionAttempt. Names are
// assumed pre-validated by the caller against the binding's deferred set: this
// function never widens authority, it only records a decision the host already
// authorized.
func (s *Session) StageToolAdmission(names []string) (AdmissionStageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	admitted := make(map[string]struct{}, len(s.admittedTools))
	for _, name := range s.admittedTools {
		admitted[name] = struct{}{}
	}
	if s.pendingAdmission != nil {
		for _, name := range s.pendingAdmission.Names {
			admitted[name] = struct{}{}
		}
	}
	var result AdmissionStageResult
	for _, name := range names {
		if _, ok := admitted[name]; ok {
			result.Already = append(result.Already, name)
			continue
		}
		admitted[name] = struct{}{}
		result.Staged = append(result.Staged, name)
	}
	if len(result.Staged) == 0 {
		return result, nil
	}
	// Charge the publication bound once per staged batch: one boundary
	// publication serves every name staged during the turn.
	if s.pendingAdmission == nil && s.admissionPublications >= tools.MaxAdmissionPublications {
		return AdmissionStageResult{}, fmt.Errorf("tool loading is exhausted for this agent: %d surface widenings already made (limit %d)", s.admissionPublications, tools.MaxAdmissionPublications)
	}
	if s.pendingAdmission == nil {
		s.pendingAdmission = &AdmissionStage{SurfaceGeneration: s.agentSurfaceGeneration}
	}
	// Ownership moves to the turn that last touched the stage: a folded stage
	// carries this turn's names too, so this turn's boundary is the one
	// entitled to publish or discard it.
	s.pendingAdmission.TurnID = s.turnID
	s.pendingAdmission.Names = append(s.pendingAdmission.Names, result.Staged...)
	return result, nil
}

// dropPendingAdmissionForTurn discards a stage whose turn errored or was
// discarded. The names remain deferred and the model may ask again.
//
// The turn id is the whole point: a stage legitimately outlives its staging
// turn (a deferral keeps it pending across turn boundaries), so an unrelated
// later turn's failure must not destroy a retry another turn was promised.
func (s *Session) dropPendingAdmissionForTurn(turnID uint64) {
	s.mu.Lock()
	if s.pendingAdmission != nil && s.pendingAdmission.TurnID == turnID {
		s.pendingAdmission = nil
	}
	s.mu.Unlock()
}

// PublishPendingAdmission attempts the turn-boundary surface publication for a
// stage recorded during turn. It is called after the turn's history is durably
// committed, so the generation bump can never fence that turn out of its own
// persistence (plan tools/05 D6 ordering).
//
// A stage that cannot publish now stays pending for the next qualifying
// boundary; a stage whose binding has been replaced is dropped.
func (s *Session) PublishPendingAdmission() {
	s.mu.Lock()
	stage := s.pendingAdmission
	widener := s.surfaceWidener
	if stage == nil {
		s.mu.Unlock()
		return
	}
	if widener == nil || stage.SurfaceGeneration != s.agentSurfaceGeneration {
		// The binding this stage was authored against is gone (/agent switch),
		// or no host publisher exists. Either way the stage is void.
		s.pendingAdmission = nil
		s.mu.Unlock()
		return
	}
	// Sole-active-turn is what makes closing the old dispatcher safe: no
	// sibling turn and no background run can still be executing on it (R2-1).
	// A superseded or errored staging turn never reaches here at all - both
	// paths drop the stage before the boundary - so a stage that survives to a
	// quiet boundary is publishable even if that boundary belongs to a later
	// turn than the one that staged it.
	if s.switching || s.activeTurns != 1 || stage.TurnID > s.turnID {
		s.deferAdmissionLocked()
		s.mu.Unlock()
		return
	}
	admitted := append(slices.Clone(s.admittedTools), stage.Names...)
	generation := s.agentSurfaceGeneration
	// Require the CURRENT turn, not the staging one: the atomic re-check in
	// TryPublishAgentSurface exists to catch a turn starting between this
	// check and the swap, which is the R2-1 hazard.
	turnID := s.turnID
	prompt, maxSteps := s.SystemPrompt, s.MaxSteps
	s.mu.Unlock()

	// Background orchestration that outlives the turn still owns this
	// session's dispatcher; widening would close it underneath (R2-2).
	if err := s.CheckSwitchAllowed(); err != nil {
		s.mu.Lock()
		s.deferAdmissionLocked()
		s.mu.Unlock()
		return
	}
	published, err := widener(admitted, AgentSurfacePublication{
		Prompt: prompt, MaxSteps: maxSteps,
		RequireTurnID: turnID, RequireSurfaceGeneration: generation,
		RequireSoleActiveTurn: true,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || !published {
		s.deferAdmissionLocked()
		return
	}
	s.admittedTools = admitted
	s.pendingAdmission = nil
	s.admissionPublications++
	s.admissionDeferrals = 0
}

// deferAdmissionLocked keeps a stage pending and, up to a bounded count, tells
// the user why their tools have not appeared yet.
func (s *Session) deferAdmissionLocked() {
	s.admissionDeferrals++
	if s.admissionDeferrals > maxAdmissionDeferralNotes || s.pendingAdmission == nil {
		return
	}
	s.admissionNotes = append(s.admissionNotes,
		fmt.Sprintf("tool loading deferred: %s could not be added to the tool surface yet (other work is still active); it will be retried at the next turn boundary",
			strings.Join(s.pendingAdmission.Names, ", ")))
}

// TakeAdmissionNotes drains and returns queued operator-visible admission
// notes. The host prints them; leaving them in the session would repeat them.
func (s *Session) TakeAdmissionNotes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	notes := s.admissionNotes
	s.admissionNotes = nil
	return notes
}

// ResetAdmissions clears every admission decision for a new agent binding.
// An /agent switch resets the surface to that agent's core tier (D4).
func (s *Session) ResetAdmissions() {
	s.mu.Lock()
	s.admittedTools = nil
	s.pendingAdmission = nil
	s.admissionPublications = 0
	s.admissionAttempts = 0
	s.admissionDeferrals = 0
	s.mu.Unlock()
}

// TryPublishAgentSurface publishes a pre-built agent surface only while every
// stated precondition still holds, verified under one acquisition of the
// session lock together with the swap itself. It reports whether the
// publication happened; on false the caller owns closing the candidate
// dispatcher, which was never installed.
//
// Checking preconditions and publishing atomically is the whole point: a
// separate check would let a force-sent sibling turn start in the gap and have
// its dispatcher closed underneath it (plan tools/05 R2-1).
func (s *Session) TryPublishAgentSurface(pub AgentSurfacePublication) bool {
	s.mu.Lock()
	if s.switching ||
		(pub.RequireSoleActiveTurn && s.activeTurns != 1) ||
		(pub.RequireTurnID != 0 && pub.RequireTurnID != s.turnID) ||
		(pub.RequireSurfaceGeneration != 0 && pub.RequireSurfaceGeneration != s.agentSurfaceGeneration) {
		s.mu.Unlock()
		return false
	}
	old := s.binding.Dispatcher
	s.agentSurfaceGeneration++
	s.SystemPrompt = pub.Prompt
	s.MaxSteps = pub.MaxSteps
	s.Tools = pub.Registry
	s.Dispatcher = pub.Dispatcher
	s.binding.Dispatcher = pub.Dispatcher
	s.binding.SkillRegistry = pub.SkillRegistry
	s.binding.AgentSurfaceGeneration = s.agentSurfaceGeneration
	s.invalidateLocked()
	setSystemMessageLocked(s, pub.Prompt)
	s.mu.Unlock()
	if old != nil && old != pub.Dispatcher {
		old.Close()
	}
	return true
}
