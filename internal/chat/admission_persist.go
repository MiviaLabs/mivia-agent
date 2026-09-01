package chat

import (
	"context"
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// SetAdmissionBinding records the identity a persisted admitted set is keyed
// by: the selected agent's name and the digest of its core/deferred tier split.
// The host sets it whenever it publishes an agent binding.
func (s *Session) SetAdmissionBinding(agentName, digest string) {
	s.mu.Lock()
	s.admissionAgent = agentName
	s.admissionDigest = digest
	s.mu.Unlock()
}

// admissionRecord snapshots what should be persisted with the session.
func (s *Session) admissionRecord() contextstate.SessionAdmission {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return contextstate.SessionAdmission{
		Agent:  s.admissionAgent,
		Digest: s.admissionDigest,
		Names:  slices.Clone(s.admittedTools),
	}
}

// persistAdmission writes the admitted set to the durable context catalog. A
// session with no context catalog configured is a no-op - there is nowhere
// durable to keep the record.
func (s *Session) persistAdmission(name string) error {
	record := s.admissionRecord()
	catalog, principal, ok := s.admissionCatalog()
	if !ok {
		return nil
	}
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	if !instance.IsZero() {
		if scoped, ok := catalog.(contextstate.WorktreeAdmissionCatalog); ok {
			return scoped.SaveWorktreeSessionAdmission(context.Background(), principal, name, record, instance)
		}
		return contextstate.ErrWorktreeDeleted
	}
	return catalog.SaveSessionAdmission(context.Background(), principal, name, record)
}

// loadAdmission reads back the persisted set from the durable context catalog.
func (s *Session) loadAdmission(name string) (contextstate.SessionAdmission, error) {
	catalog, principal, ok := s.admissionCatalog()
	if !ok {
		return contextstate.SessionAdmission{}, nil
	}
	s.mu.RLock()
	instance := s.contextWorktree
	s.mu.RUnlock()
	if !instance.IsZero() {
		if scoped, ok := catalog.(contextstate.WorktreeAdmissionCatalog); ok {
			return scoped.LoadWorktreeSessionAdmission(context.Background(), principal, name, instance)
		}
		return contextstate.SessionAdmission{}, contextstate.ErrWorktreeDeleted
	}
	return catalog.LoadSessionAdmission(context.Background(), principal, name)
}

func (s *Session) admissionCatalog() (contextstate.SessionAdmissionCatalog, contextstate.Principal, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	catalog, ok := s.contextStore.(contextstate.SessionAdmissionCatalog)
	return catalog, s.contextPrincipal, ok && s.contextEnabledLocked()
}

// replayAdmission re-applies a persisted admitted set to the freshly loaded
// session, synchronously and before the session can issue its first request
// (plan tools/05 D3/R2-3). It runs on every load site because every load site
// funnels through Session.Load.
//
// A record whose agent or tier digest no longer matches is dropped rather than
// trusted: the same tool name under a different tier split is not necessarily
// the same authority decision. The drop is announced, because silently
// resuming with fewer tools than the transcript shows being used is the kind of
// difference a user has no way to discover.
func (s *Session) replayAdmission(name string) {
	record, err := s.loadAdmission(name)
	if err != nil || len(record.Names) == 0 {
		prior := s.AdmittedTools()
		s.ResetAdmissions()
		// The record admits nothing, so the live surface must say the same. A
		// successful narrowing is silent: a resume that narrows is not a
		// surprise the user must be warned about. A refused one is not, because
		// the tools stay live and the user would otherwise never know.
		if !s.narrowSurfaceToCore(prior) {
			s.noteAdmissionRetained(prior)
		}
		return
	}
	s.mu.Lock()
	agent, digest := s.admissionAgent, s.admissionDigest
	hasWidener := s.surfaceWidener != nil
	prior := slices.Clone(s.admittedTools)
	s.admittedTools = nil
	s.pendingAdmission = nil
	s.mu.Unlock()
	if record.Agent != agent || record.Digest != digest || !hasWidener {
		s.noteDroppedOrRetained(record.Names, prior)
		return
	}
	if !s.republishSurface(slices.Clone(record.Names)) {
		s.noteDroppedOrRetained(record.Names, prior)
		return
	}
	s.mu.Lock()
	s.admittedTools = slices.Clone(record.Names)
	s.mu.Unlock()
}

// republishSurface asks the host to rebuild the root surface with admitted
// appended to the core tier, under the D7 preconditions that apply to a load.
// RequireSoleActiveTurn is deliberately not among them - a load happens with
// no turn active - but the surface generation and the switch guard are: this
// call can Close the live dispatcher, and background orchestration that
// outlives the chat turn still owns it (R2-2).
//
// It reports whether the surface was actually republished.
func (s *Session) republishSurface(admitted []string) bool {
	s.mu.RLock()
	widener := s.surfaceWidener
	// BaseSystemPrompt, not SystemPrompt (plan 77, E3) - see admission_status.go's
	// identical fix for why: this feeds AgentSurfacePublication.Prompt,
	// which gets recomposed with the memory block fresh.
	prompt, maxSteps := s.BaseSystemPrompt, s.MaxSteps
	generation := s.agentSurfaceGeneration
	s.mu.RUnlock()
	if widener == nil {
		return false
	}
	if err := s.CheckSwitchAllowed(); err != nil {
		return false
	}
	published, err := widener(admitted, AgentSurfacePublication{
		Prompt: prompt, MaxSteps: maxSteps,
		RequireSurfaceGeneration: generation,
	})
	return err == nil && published
}

// narrowSurfaceToCore rebuilds a core-only surface when the session was
// carrying admitted tools, so the registry never advertises more than the
// admitted set the session reports and persists. It is a no-op when the live
// surface is already core-only, so an ordinary resume never churns the
// dispatcher. It reports whether the live surface is core-only afterwards.
//
// The refusal path is the point: republishSurface returns false whenever the
// host cannot rebuild right now (no widener, switch guard, rebuild error,
// refused publication), and by then the caller has already cleared
// admittedTools. Leaving it cleared would make the session report - and
// persist - an empty set while the registry still advertises and can invoke
// the tools. So when the tools are demonstrably still live, the reported set
// is restored: fail closed on the reporting side, never claiming less than the
// surface can actually do.
//
// The live registry, not the refusal, is the test. Reporting a tool that is
// NOT live would be the opposite lie and a worse one: load_tools would answer
// "already loaded" for a tool the model cannot call, with no way out.
func (s *Session) narrowSurfaceToCore(prior []string) bool {
	if len(prior) == 0 {
		return true
	}
	if s.republishSurface(nil) {
		return true
	}
	if !s.surfaceAdvertisesAny(prior) {
		return true
	}
	s.mu.Lock()
	s.admittedTools = slices.Clone(prior)
	s.mu.Unlock()
	return false
}

// surfaceAdvertisesAny reports whether any of names is still registered on the
// live tool surface.
func (s *Session) surfaceAdvertisesAny(names []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Tools == nil {
		return false
	}
	for _, name := range names {
		if _, ok := s.Tools.Get(name); ok {
			return true
		}
	}
	return false
}

// noteDroppedOrRetained narrows the live surface and then says what actually
// happened. The two outcomes are opposites - the tools are gone, or they are
// still there - and telling the user the wrong one is worse than saying
// nothing, so the note is chosen after the narrowing, never before it.
func (s *Session) noteDroppedOrRetained(recorded, prior []string) {
	if s.narrowSurfaceToCore(prior) {
		s.noteAdmissionDrop(recorded)
		return
	}
	s.noteAdmissionRetained(prior)
}

// noteAdmissionRetained reports tools that could not be taken back off the live
// surface. They remain loaded and invocable, and the session keeps reporting
// them, so the user is told rather than left with a silent divergence.
func (s *Session) noteAdmissionRetained(names []string) {
	s.mu.Lock()
	s.admissionNotes = append(s.admissionNotes,
		fmt.Sprintf("previously loaded tools could not be removed from the live tool surface (other work is still active), so they stay loaded for now: %s.",
			boundedNames(names, maxAdmissionNoteNames)))
	s.mu.Unlock()
}

func (s *Session) noteAdmissionDrop(names []string) {
	s.mu.Lock()
	s.admissionNotes = append(s.admissionNotes,
		fmt.Sprintf("previously loaded tools were not restored because this session's tool configuration changed: %s. Load them again if you still need them.",
			boundedNames(names, maxAdmissionNoteNames)))
	s.mu.Unlock()
}
