package chat

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
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

// captureTurnToken captures the current operation fence and pins TurnID to the
// given turn id. captureOperationTokenLocked reads the session's CURRENT turn
// id; a turn that re-captures its fence after a start-of-turn surface
// publication (surfaceForTurnStart) must keep its own id, so a superseding
// turn's id can never validate the older turn's commit
// (chat-turnstart-admission-fences-own-turn).
func (s *Session) captureTurnToken(turnID uint64) OperationToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	token := s.captureOperationTokenLocked(fmt.Sprintf("turn:%d", turnID))
	token.TurnID = turnID
	return token
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

// logStaleOperation reports an ErrStaleOperation/ErrStaleAutosave outcome
// that every caller in this package otherwise treats as an intentional no-op
// (a superseded turn losing the persistence race to whatever superseded it -
// see ErrStaleOperation's doc comment). Intentional and silent are not the
// same thing: a turn dropped because the fence was genuinely stale looks
// identical, from the outside, to a turn dropped because the fence was
// MISCOMPUTED - e.g. a resume/reclaim minting a baseline that misclassifies
// the newest (and only) copy of a turn's history as stale. Before this,
// callers discarded the error with no trace anywhere, so that second case -
// real data loss - was indistinguishable from ordinary, harmless supersession
// and never left evidence to diagnose it by. This does not fix a miscomputed
// fence; it makes one observable the next time it happens instead of silent.
func logStaleOperation(where string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "\n⚠ %s: turn history was not persisted (%v). This is expected when a newer turn superseded this one; if no newer turn was in flight, this is a bug and history may have been lost.\n", where, err)
}

// SaveAfterTurn saves the session as an auto-save without pruning. It is
// fenced so a clear, load, switch, or newer turn cannot publish stale state.
func (s *Session) SaveAfterTurn() {
	s.autoSaveContextSession()
}

// saveAfterTurn is a permanent no-op, for the same reason SaveLast is: the
// legacy file-store's own per-turn crash-recovery mechanism (SessionDir,
// saveManager) is gone. Its remaining callers, persistPlainLegacyTurn and
// commitPreparedTurn, are themselves unreachable in production - every real
// session is context-enabled, and finishAgentTurn / the plain-turn path both
// take the context-catalog branch instead - so this was already the
// production behavior before this change: SessionDir and saveManager were
// always unset for a context-enabled session, and this method's original
// guard already returned nil on every real invocation.
func (s *Session) saveAfterTurn(token OperationToken) error {
	return nil
}

// autoSaveContextSession promotes a live context-backed session into the
// named catalog under its own SessionID, the same path /save already
// exercises (saveContextSession), so it appears in "sessions list"
// immediately after the first turn instead of only once someone runs
// /save or renames it. SessionID is stable for the session's lifetime, so
// repeated calls upsert the same catalog row rather than creating new ones.
// Best-effort: a transient catalog write failure here must not fail the
// turn that triggered it, so errors are reported, not returned.
func (s *Session) autoSaveContextSession() {
	if err := s.Save(s.SessionID); err != nil {
		fmt.Fprintf(os.Stderr, "\n⚠ turn auto-save failed: %v\n", err)
	}
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
