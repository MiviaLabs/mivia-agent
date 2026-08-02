package chat

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// maxAdmissionDeferralNotes bounds how many "still not loaded" notes one agent
// binding may surface. A stage that keeps missing its publication boundary is
// worth saying once, not once per turn.
const maxAdmissionDeferralNotes = 2

// maxConsecutiveAdmissionNoOps bounds how many times in a row a model may ask
// for tools it already has. Such a call is refunded against the attempt budget
// (StageToolAdmission), and that refund is precisely what makes a bound
// necessary: a free call can be repeated forever.
const maxConsecutiveAdmissionNoOps = 3

// AdmissionStage is one turn's recorded intent to widen the tool surface.
// load_tools executes inside a turn and cannot rebuild the surface it is
// running on (plan tools/05 D6/F2), so it records intent here and the turn
// boundary performs the publication.
type AdmissionStage struct {
	// Names are the deferred tools to admit, in the order they were staged.
	Names []string
	// nameTurnIDs is parallel to Names: entry i is the turn that staged
	// Names[i]. Ownership is per NAME, not per stage, because a stage that
	// defers is folded into by later turns - a whole-stage owner would let one
	// appended name transfer, and then destroy, another turn's promised retry.
	nameTurnIDs []uint64
	// SurfaceGeneration is the agent-surface generation captured at staging.
	// A stage whose generation no longer matches is dropped: it was authored
	// against a binding that an /agent switch has since replaced. A model
	// switch preserves the generation, so a stage survives one.
	SurfaceGeneration uint64
	// TurnID is the turn that most recently staged into this stage, taken from
	// the dispatcher's caller frame rather than from the session's current turn
	// id: under force-send a superseding turn has already bumped the latter, so
	// stamping with it hands the stage to a turn that never staged anything.
	// It is a report of the last stager; the drop decision uses nameTurnIDs.
	TurnID uint64
}

// turnIDPrefix is the caller-frame turn id format produced by sendAgent.
const turnIDPrefix = "turn:"

// TurnIDFromContext reports the session turn a tool call is executing under.
//
// The dispatcher stamps the caller frame from the turn's own Request (see
// sendAgent's opts.TurnID), so this is the id of the turn that is really
// running the call - which is not the session's current turn id once a
// force-sent turn has superseded it. It is host-set, never model-supplied.
func TurnIDFromContext(ctx context.Context) (uint64, bool) {
	caller, ok := runtime.CallerFrom(ctx)
	if !ok {
		return 0, false
	}
	digits, ok := strings.CutPrefix(caller.TurnID, turnIDPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
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
	stage.nameTurnIDs = slices.Clone(stage.nameTurnIDs)
	return stage, true
}

// AdmissionStageResult describes what one load_tools call did.
type AdmissionStageResult struct {
	// Staged are names newly recorded for admission at the next boundary.
	Staged []string
	// Already are names already PUBLISHED into the surface: callable right now.
	// They are free: they consume no publication budget.
	Already []string
	// AlreadyStaged are names staged by an earlier call but not yet published.
	// They are free too, but they are NOT callable yet - publication happens at
	// a turn boundary (D6). Keeping them apart from Already is what stops the
	// result telling the model to call a tool that does not exist yet.
	AlreadyStaged []string
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
// turnID is the turn executing the call (TurnIDFromContext), not the session's
// current turn: it becomes the stage's owner. Zero means "no owning turn" -
// no turn boundary will drop such a stage, which is the right answer for an
// out-of-band caller, because no turn's failure discards it.
//
// It charges the publication bound only when the call actually stages
// something new (plan tools/05 F7), so an idempotent re-request is free. The
// attempt bound is charged by the caller via ChargeAdmissionAttempt; a call
// that turns out to be a pure no-op is refunded here, because the frozen index
// (D8) keeps advertising loaded tools as loadable and so invites exactly that
// call - it must not consume the budget a genuine request needs. Names are
// assumed pre-validated by the caller against the binding's deferred set: this
// function never widens authority, it only records a decision the host already
// authorized.
func (s *Session) StageToolAdmission(names []string, turnID uint64) (AdmissionStageResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Published and staged are tracked SEPARATELY: both make a re-request free,
	// but only the published set is callable now. Folding them together is what
	// made the result and the streak error advertise a tool the model cannot
	// call until the next turn boundary (D6).
	published := make(map[string]struct{}, len(s.admittedTools))
	for _, name := range s.admittedTools {
		published[name] = struct{}{}
	}
	staged := make(map[string]struct{})
	if s.pendingAdmission != nil {
		for _, name := range s.pendingAdmission.Names {
			staged[name] = struct{}{}
		}
	}
	var result AdmissionStageResult
	for _, name := range names {
		if _, ok := published[name]; ok {
			result.Already = append(result.Already, name)
			continue
		}
		if _, ok := staged[name]; ok {
			result.AlreadyStaged = append(result.AlreadyStaged, name)
			continue
		}
		staged[name] = struct{}{}
		result.Staged = append(result.Staged, name)
	}
	if len(result.Staged) == 0 {
		if len(result.Already) == 0 && len(result.AlreadyStaged) == 0 {
			// Nothing was asked for; there is nothing to refund or to count.
			return result, nil
		}
		s.admissionNoOps++
		if s.admissionNoOps > maxConsecutiveAdmissionNoOps {
			return AdmissionStageResult{}, noOpStreakError(result)
		}
		// Refund the attempt the host charged before parsing: re-asking for a
		// tool you already have is a mistake the frozen index invites, not the
		// abuse the attempt bound exists to stop. The refund budget is
		// per-binding, not per-streak: a per-streak refund is replenished by
		// every genuine call, which multiplies the stated attempt bound
		// instead of merely absorbing the invited mistakes.
		if s.admissionRefunds < maxConsecutiveAdmissionNoOps && s.admissionAttempts > 0 {
			s.admissionAttempts--
			s.admissionRefunds++
		}
		return result, nil
	}
	// Charge the publication bound once per staged batch: one boundary
	// publication serves every name staged during the turn.
	if s.pendingAdmission == nil && s.admissionPublications >= tools.MaxAdmissionPublications {
		// Rejected before the streak is cleared: a call that can never succeed
		// must not hand back a fresh run of free no-ops.
		return AdmissionStageResult{}, fmt.Errorf("tool loading is exhausted for this agent: %d surface widenings already made (limit %d)", s.admissionPublications, tools.MaxAdmissionPublications)
	}
	s.admissionNoOps = 0
	if s.pendingAdmission == nil {
		s.pendingAdmission = &AdmissionStage{SurfaceGeneration: s.agentSurfaceGeneration}
	}
	// TurnID reports the last stager. The per-name owners below are what the
	// drop consults, so folding into another turn's stage never transfers it.
	s.pendingAdmission.TurnID = turnID
	s.pendingAdmission.Names = append(s.pendingAdmission.Names, result.Staged...)
	for range result.Staged {
		s.pendingAdmission.nameTurnIDs = append(s.pendingAdmission.nameTurnIDs, turnID)
	}
	return result, nil
}

// noOpStreakError is the corrective message a looping model receives. It must
// be exact about which names are callable now and which only become callable
// at the next turn: it is the only signal the model gets, and a wrong one sends
// it straight into an unknown-tool failure.
func noOpStreakError(result AdmissionStageResult) error {
	var parts []string
	if len(result.Already) > 0 {
		parts = append(parts, fmt.Sprintf("%s: already loaded and callable now",
			strings.Join(result.Already, ", ")))
	}
	if len(result.AlreadyStaged) > 0 {
		parts = append(parts, fmt.Sprintf("%s: already staged and callable from your next turn, not this one",
			strings.Join(result.AlreadyStaged, ", ")))
	}
	return fmt.Errorf("%s. Stop calling load_tools for them. The list of not-loaded tools in your instructions is frozen from when this agent was bound and is never updated as tools load",
		strings.Join(parts, "; "))
}

// dropPendingAdmissionForTurn discards a stage whose turn errored or was
// discarded. The names remain deferred and the model may ask again.
//
// The turn id is the whole point: a stage legitimately outlives its staging
// turn (a deferral keeps it pending across turn boundaries), so an unrelated
// later turn's failure must not destroy a retry another turn was promised.
// Because a deferred stage is folded into by whatever turn stages next, the
// filter is per NAME: only this turn's own entries go, and the stage survives
// while any other turn's name remains.
func (s *Session) dropPendingAdmissionForTurn(turnID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stage := s.pendingAdmission
	if stage == nil {
		return
	}
	names := make([]string, 0, len(stage.Names))
	owners := make([]uint64, 0, len(stage.nameTurnIDs))
	for i, name := range stage.Names {
		if i < len(stage.nameTurnIDs) && stage.nameTurnIDs[i] == turnID {
			continue
		}
		names = append(names, name)
		owners = append(owners, stage.nameTurnIDs[i])
	}
	if len(names) == 0 {
		s.pendingAdmission = nil
		return
	}
	stage.Names = names
	stage.nameTurnIDs = owners
	stage.TurnID = owners[len(owners)-1]
}

// resetAdmissionNoOps clears the consecutive-no-op streak at a turn boundary.
// The counter is documented as CONSECUTIVE; without this it is a per-binding
// lifetime counter, and one innocent re-request per turn hard-errors after a
// handful of turns.
func (s *Session) resetAdmissionNoOps() {
	s.mu.Lock()
	s.admissionNoOps = 0
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
	// It is also what makes publishing another turn's stage safe: the owning
	// turn is either still active - in which case this boundary defers - or it
	// has already reached its own boundary, where a superseded or errored turn
	// drops its stage. So a stage that survives to a quiet boundary is
	// publishable even when that boundary belongs to a later turn.
	//
	// There is deliberately no stage.TurnID > s.turnID check: turn ids are
	// monotonic and a stage is stamped with a turn that has already been
	// issued, so the condition is unreachable. Asserting it would only look
	// like a defence.
	if s.switching || s.activeTurns != 1 {
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
	s.admissionNoOps = 0
	s.admissionRefunds = 0
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
