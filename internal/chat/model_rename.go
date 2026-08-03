package chat

import (
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// RenameModel points a binding at a different model name without resolving a
// new profile, and drops the reasoning surface that belonged to the model being
// renamed away from.
//
// The reset is not cosmetic. An empty dialect does not mean "send no reasoning
// fields": provider.OpenAICompat falls back to the client's own default dialect
// (zai thinks, openrouter speaks openai), so a stale dial, dialect, or declared
// set puts reasoning fields on the wire for a model that never declared any.
// The declared set matters on its own - Session.SetReasoningEffort validates
// against it, so a stale set makes /effort accept a level the model does not
// offer.
//
// Every path that renames a selection in place must go through here (or through
// Session.renameModelLocked, which adds the session-scoped half) so the reset
// cannot drift between them.
func (b *ModelBinding) RenameModel(name string) {
	if name == b.Model {
		// Not a rename: the profile still describes this model, and wiping its
		// reasoning surface would silently disarm a model that does declare one.
		b.Profile.Name = name
		return
	}
	b.Model = name
	b.Profile.Name = name
	b.Profile.Reasoning = ""
	b.Profile.ReasoningDialect = ""
	b.Profile.ReasoningEfforts = nil
}

// renameModelLocked applies a rename to the published binding. The user's
// /effort choice goes with it: it was chosen for the previous model, and the
// new one may not offer that level at all.
func (s *Session) renameModelLocked(name string) {
	renamed := name != s.binding.Model
	s.binding.RenameModel(name)
	s.model = name
	if renamed {
		s.reasoningEffort = ""
	}
}

// SelectModel changes the selected model when it is safe and permitted by the
// session's immutable provider policy.
func (s *Session) SelectModel(name string) bool {
	name, err := config.NormalizeModelName(name)
	if err != nil {
		return false
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.activeTurns > 0 || s.switching {
		s.mu.Unlock()
		return false
	}
	if len(s.allowedModels) > 0 && !slices.Contains(s.allowedModels, name) {
		s.mu.Unlock()
		return false
	}
	newBinding := s.binding
	newBinding.Model = name
	if newBinding.ModelGeneration == 0 {
		newBinding.ModelGeneration = 1
	} else {
		newBinding.ModelGeneration++
	}
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextExpected := s.contextHead
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	expectedBinding := captureBindingRevision(s.binding)
	newBindingRevision := captureBindingRevision(newBinding)
	if contextEnabled {
		s.mu.Unlock()
		if err := s.advanceContextHead(contextStore, contextPrincipal, contextExpected, expectedBinding, newBindingRevision, "select", false); err != nil {
			return false
		}
		s.mu.Lock()
	}
	// SelectModel renames the selection without resolving a new profile, so
	// everything model-specific still on it describes the PREVIOUS model.
	s.renameModelLocked(name)
	s.binding.ModelGeneration = newBinding.ModelGeneration
	s.invalidateLocked()
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	s.mu.Unlock()
	return true
}

// ModelRestoreNotice returns a snapshot of a rejected saved model and the
// current selected model. A non-nil rejected value can be empty.
func (s *Session) ModelRestoreNotice() (saved, current string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.rejectedSavedModel == nil {
		return "", "", false
	}
	return *s.rejectedSavedModel, s.model, true
}

func (s *Session) restoreModelLocked(saved string) {
	s.rejectedSavedModel = nil
	normalized, err := config.NormalizeModelName(saved)
	if err == nil && (len(s.allowedModels) == 0 || slices.Contains(s.allowedModels, normalized)) {
		// A restore renames the selection without resolving a new profile, so it
		// owes the same reset as SelectModel.
		s.renameModelLocked(normalized)
		return
	}
	saved = strings.TrimSpace(saved)
	s.rejectedSavedModel = &saved
}
