package chat

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

var (
	ErrStaleOperation = errors.New("stale chat operation")
	ErrStaleAutosave  = errors.New("stale chat autosave")
	ErrPersistence    = errors.New("chat persistence failed")
)

// BindingFence is the immutable provider/model identity captured by work that
// may publish after provider I/O.
type BindingFence struct {
	ProviderName           string
	Model                  string
	ModelGeneration        uint64
	AgentSurfaceGeneration uint64
}

// OperationToken fences an asynchronous operation against every mutable
// session domain that can invalidate its result.
type OperationToken struct {
	Epoch          uint64
	Revision       contextstate.Revision
	Binding        BindingFence
	TurnID         uint64
	SourceRange    contextstate.SourceRange
	IdempotencyKey string
}

// SaveToken is an alias so SaveManager callers can describe the same fence
// without gaining access to session mutation capabilities.
type SaveToken = OperationToken

func (t OperationToken) zero() bool {
	return t.Epoch == 0 && t.Revision == (contextstate.Revision{}) && t.TurnID == 0 && t.Binding == (BindingFence{})
}

func (t OperationToken) sameFence(other OperationToken) bool {
	return t.Epoch == other.Epoch && t.Revision == other.Revision && t.Binding == other.Binding && t.TurnID == other.TurnID && t.SourceRange == other.SourceRange
}

func (t OperationToken) newerThan(other OperationToken) bool {
	for _, pair := range [][2]uint64{{t.Epoch, other.Epoch}, {t.Revision.Session, other.Revision.Session}, {t.Revision.Durable, other.Revision.Durable}, {t.TurnID, other.TurnID}} {
		if pair[0] != pair[1] {
			return pair[0] > pair[1]
		}
	}
	return false
}

func (t OperationToken) String() string {
	return fmt.Sprintf("epoch=%d session=%d durable=%d turn=%d binding=%d", t.Epoch, t.Revision.Session, t.Revision.Durable, t.TurnID, t.Binding.ModelGeneration)
}

func (s *Session) invalidateLocked() {
	s.operationEpoch++
	s.contextRevision.Session++
}

func (s *Session) captureOperationTokenLocked(key string) OperationToken {
	return OperationToken{Epoch: s.operationEpoch, Revision: s.contextRevision, Binding: captureBindingFence(s.binding), TurnID: s.turnID, IdempotencyKey: key}
}

func (s *Session) captureOperationToken(key string) OperationToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.captureOperationTokenLocked(key)
}

func (s *Session) tokenCurrentLocked(token OperationToken) bool {
	return token.sameFence(s.captureOperationTokenLocked(token.IdempotencyKey))
}

func captureBindingFence(binding ModelBinding) BindingFence {
	return BindingFence{ProviderName: binding.ProviderName, Model: binding.Model, ModelGeneration: binding.ModelGeneration, AgentSurfaceGeneration: binding.AgentSurfaceGeneration}
}

// captureBindingRevision adapts the chat binding to the durable contract.
func captureBindingRevision(binding ModelBinding) contextstate.BindingRevision {
	return contextstate.BindingRevision{Provider: binding.ProviderName, Model: binding.Model, Generation: binding.ModelGeneration}
}

func (s *Session) captureBindingRevision() contextstate.BindingRevision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return captureBindingRevision(s.binding)
}

func (s *Session) currentSaveToken() OperationToken {
	return s.captureOperationToken("")
}

// SaveAfterTurn saves the session as an auto-save without pruning. It is
// fenced so a clear, load, switch, or newer turn cannot publish stale state.
func (s *Session) SaveAfterTurn() {
	if s.ContextEnabled() {
		return
	}
	if s.SessionDir == "" && s.saveManager == nil {
		return
	}
	s.mu.Lock()
	s.captureBindingLocked()
	token := s.captureOperationTokenLocked("manual-save")
	s.mu.Unlock()
	if err := s.saveAfterTurn(token); err != nil && !errors.Is(err, ErrStaleOperation) && !errors.Is(err, ErrStaleAutosave) {
		fmt.Fprintf(os.Stderr, "\n⚠ turn auto-save failed: %v\n", err)
	}
}

func (s *Session) saveAfterTurn(token OperationToken) error {
	if s.SessionDir == "" && s.saveManager == nil {
		return nil
	}
	s.mu.Lock()
	s.captureBindingLocked()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	// A lone user message is real content (TestHasContent_UserOnly): the
	// per-turn crash snapshot must not drop the question just because the
	// transcript has no system prompt and no assistant reply yet.
	hasContent := hasContent(msgs)
	s.mu.Unlock()
	if !hasContent {
		return nil
	}
	if s.saveManager != nil {
		if err := s.saveManager.SaveAfterTurnWithRevision(msgs, token); err != nil {
			if errors.Is(err, ErrStaleAutosave) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrPersistence, err)
		}
		s.markDurableRevision(token)
		return nil
	}
	return s.saveLegacyTurn(token, msgs)
}

func (s *Session) saveLegacyTurn(token OperationToken, msgs []provider.Message) error {
	s.mu.Lock()
	if !s.tokenCurrentLocked(token) {
		s.mu.Unlock()
		return ErrStaleOperation
	}
	if s.turnSaveName == "" {
		s.turnSaveName = uniqAutoSaveName(s.SessionDir, turnSaveMarker)
	}
	name := s.turnSaveName
	s.mu.Unlock()
	if err := s.Save(name); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	s.markDurableRevision(token)
	return nil
}

// isTurnToken reports whether the token belongs to a turn lifecycle (captured
// by beginAgentTurn/beginPlainTurn with the "turn:N" key) rather than to a
// benign host autosave (TUI periodic save, /clear, SaveLast) whose key is
// "manual-save" or empty. Only turn-lifecycle tokens may advance the durable
// domain.
func isTurnToken(token OperationToken) bool {
	return strings.HasPrefix(token.IdempotencyKey, "turn:")
}

// markDurableRevision advances the durable domain only for turn-lifecycle
// saves whose fence is still current. Benign host-triggered autosaves snapshot
// the same state and must not advance it: if they did, an in-flight turn's
// captured token would be fenced out of commitPreparedTurn's adoption and
// persistence the moment such an autosave completed, silently dropping the
// turn's history. Turn-boundary commits still bump, so newerThan and the
// current-fence Durable check keep rejecting older-generation and
// same-generation-stale autosaves.
func (s *Session) markDurableRevision(token OperationToken) {
	s.mu.Lock()
	if isTurnToken(token) && s.tokenCurrentLocked(token) {
		s.contextRevision.Durable++
	}
	s.mu.Unlock()
}
