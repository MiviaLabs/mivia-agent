package chat

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ModelBinding is one immutable provider/model/backend generation.
type ModelBinding struct {
	ProviderName  string
	Model         string
	Completer     provider.Completer
	Dispatcher    *runtime.Dispatcher
	SkillRegistry *skills.Registry
	// Registry is the advertised tool surface this generation's dispatcher was
	// built against. A binding that rebuilds the dispatcher must publish it, or
	// the session would advertise tools from the previous generation that the
	// live dispatcher cannot invoke. Nil leaves the session surface untouched,
	// which is what a binding that reuses the current dispatcher wants.
	Registry              *tools.Registry
	Profile               config.ModelSpec
	RequestedPromptTokens int
	PromptBudgetTokens    int
	// ModelGeneration is a session-local monotonic binding identity. It is
	// captured with a turn and increments on every successful publication.
	ModelGeneration uint64
	// AgentSurfaceGeneration binds a prepared model dispatcher to the root
	// agent scope from which it was built. Zero is compatibility mode.
	AgentSurfaceGeneration uint64
}

// Selection identifies the provider-qualified model selected by a session.
type Selection struct {
	ProviderName string
	Model        string
}

// DefaultMaxContextTokens is the default token budget for context pruning.
// DeepSeek models support up to 1M tokens; this conservative default
// allows comfortable headroom while preventing runaway context.
const (
	DefaultMaxContextTokens = 1000000
	DefaultRequestTimeout   = 15 * time.Minute

	// DefaultMaxSteps bounds one interactive turn's agent loop when no config
	// is set. 0 (unlimited) is the default: /steps can set a per-session cap
	// if needed, and a model stuck emitting tool calls is interrupted by the
	// user, same as any other interactive tool.
	DefaultMaxSteps = 0
)

// resolvedMaxSteps honours a configured [chat] max_steps, including an explicit
// 0 (unlimited). Only an unset key falls back to the default, which is why the
// config field is a pointer.
func resolvedMaxSteps(res *config.Resolved) int {
	if res.MaxSteps != nil {
		return *res.MaxSteps
	}
	return DefaultMaxSteps
}

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	providerName := res.ProviderName
	if providerName == "" && c != nil {
		providerName = c.Name()
	}
	profile := config.ModelSpec{Name: res.Model, ContextWindowTokens: DefaultMaxContextTokens}
	for _, candidate := range res.ModelProfiles {
		if candidate.Name == res.Model {
			profile = candidate
			break
		}
	}
	operatorCap := 0
	if res.MaxPromptTokens != nil {
		operatorCap = *res.MaxPromptTokens
	}
	ctxBudget := promptBudget(profile, res.MaxTokens, operatorCap, 0)
	s := &Session{
		Completer:        c,
		model:            res.Model,
		allowedModels:    slices.Clone(res.Models),
		SystemPrompt:     res.SystemPrompt,
		Temperature:      res.Temperature,
		MaxTokens:        res.MaxTokens,
		MaxSteps:         resolvedMaxSteps(res), // /steps overrides (0 = unlimited)
		MaxContextTokens: ctxBudget,
		// 0 = uncapped; config.Load already normalized negatives and enforced
		// the 1024-byte floor for positive values.
		MaxToolResultChars: res.Tools.MaxToolResultBytes,
		// 0 = off; config.Load already normalized negatives to the derived
		// sentinel and rejected positive values under the degrade floor.
		BatchResultBudgetBytes: res.Tools.BatchResultBudgetBytes,
		SessionID:              runtime.NewSessionID(),
	}
	s.agentSurfaceGeneration = 1
	s.operatorPromptCap = operatorCap
	s.catalog = res.ModelCatalog()
	s.binding = ModelBinding{ProviderName: providerName, Model: res.Model, Completer: c, Profile: profile, PromptBudgetTokens: ctxBudget, ModelGeneration: 1}
	s.resetSystem()
	return s
}

// CurrentModel returns the selected model under the session lock.
func (s *Session) CurrentModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.Model
}

// CurrentSelection returns the provider and model from one binding snapshot.
func (s *Session) CurrentSelection() Selection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Selection{ProviderName: s.binding.ProviderName, Model: s.binding.Model}
}

// CurrentModelGeneration returns the session-local generation of the active
// provider/model binding.
func (s *Session) CurrentModelGeneration() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.ModelGeneration
}

// CurrentBinding returns the mutex-owned provider/model/backend generation
// captured as one immutable turn input. The returned pointers are generation
// objects; callers must not mutate them.
//
// The result carries the session's /effort choice in Profile.Reasoning, so it
// must never be handed back to SwitchBinding: republishing it would store the
// choice as the model's configured default, and the clear that SwitchBinding
// performs would then have nothing to fall back to. Use PublishedBinding for
// anything that round-trips.
func (s *Session) CurrentBinding() ModelBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureBindingLocked()
}

// PublishedBinding returns the published generation as CONFIGURED: the same
// snapshot CurrentBinding builds, minus the /effort fold. It is the binding a
// caller may modify and republish, because everything on it came from
// configuration and survives the round trip unchanged.
//
// The legacy-field reconciliation is done on the copy rather than on s, which
// is what lets this hold only the read lock.
func (s *Session) PublishedBinding() ModelBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding := s.binding
	if binding.Completer == nil && s.Completer != nil {
		binding.Completer = s.Completer
	}
	if binding.Dispatcher == nil && s.Dispatcher != nil {
		binding.Dispatcher = s.Dispatcher
	}
	if s.model != "" {
		binding.Model = s.model
	}
	return binding
}

// SwitchBinding atomically publishes a fully prepared idle binding.
func (s *Session) SwitchBinding(binding ModelBinding) error {
	name, err := config.NormalizeModelName(binding.Model)
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return err
	}
	if strings.TrimSpace(binding.ProviderName) == "" || binding.Completer == nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return fmt.Errorf("incomplete model binding")
	}
	binding.Model = name
	current, err := s.switchPreflight()
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return err
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.activeTurns > 0 {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("model switching is unavailable while work is active")
	}
	if s.switching {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("model switching is unavailable while session surface is changing")
	}
	if binding.AgentSurfaceGeneration != 0 && binding.AgentSurfaceGeneration != s.agentSurfaceGeneration {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("model binding was prepared for an outdated agent surface")
	}
	if !s.bindingAllowsLocked(binding.ProviderName, binding.Model) {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("model is not configured for provider %s", binding.ProviderName)
	}
	binding.RequestedPromptTokens = s.requestedPromptCap
	binding.PromptBudgetTokens = promptBudget(binding.Profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
	if binding.PromptBudgetTokens <= 0 {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("model has no usable prompt budget")
	}
	binding.ModelGeneration = s.binding.ModelGeneration + 1
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextExpected := s.contextHead
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	expectedBinding := captureBindingRevision(s.binding)
	newBinding := captureBindingRevision(binding)
	if err := s.advanceBindingIfNeeded(contextEnabled, contextStore, contextPrincipal, contextExpected, expectedBinding, newBinding, "switch"); err != nil {
		s.mu.Unlock()
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return fmt.Errorf("advance context binding: %w", err)
	}
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	s.mu.Unlock()
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	return nil
}

func (s *Session) switchPreflight() (*runtime.Dispatcher, error) {
	s.mu.Lock()
	if s.activeTurns > 0 {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		return current, fmt.Errorf("model switching is unavailable while work is active")
	}
	if s.switching {
		current := s.binding.Dispatcher
		s.mu.Unlock()
		return current, fmt.Errorf("model switching is unavailable while session surface is changing")
	}
	current, guard := s.binding.Dispatcher, s.switchGuard
	s.mu.Unlock()
	if guard != nil {
		if err := guard(); err != nil {
			return current, err
		}
	}
	return current, nil
}

func closeUnpublishedDispatcher(candidate, current *runtime.Dispatcher) {
	if candidate != nil && candidate != current {
		candidate.Close()
	}
}

// SetBindingFactory wires CLI-owned provider construction for exact session
// restore. The factory must prepare a complete binding without mutating s.
func (s *Session) SetBindingFactory(factory func(providerName, model string) (ModelBinding, error)) {
	s.mu.Lock()
	s.bindingFactory = factory
	s.mu.Unlock()
}

// SetSwitchGuard installs an owner callback for work that outlives the active
// chat turn. The callback can prevent replacing this session's generation
// while background orchestration still owns it.
func (s *Session) SetSwitchGuard(guard func() error) {
	s.mu.Lock()
	s.switchGuard = guard
	s.mu.Unlock()
}

// CheckSwitchAllowed reports whether owner-managed background work permits a
// session replacement or model switch.
func (s *Session) CheckSwitchAllowed() error {
	s.mu.RLock()
	guard := s.switchGuard
	s.mu.RUnlock()
	if guard != nil {
		return guard()
	}
	return nil
}

// HasActiveTurn reports whether a chat turn is currently running. Model and
// agent switches must refuse while this is true so in-flight work keeps its
// captured binding generation.
func (s *Session) HasActiveTurn() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeTurns > 0
}

// PrepareBinding delegates provider/model generation to the CLI-owned factory
// when one is installed. The boolean distinguishes an unavailable factory
// from a factory that attempted construction and failed.
func (s *Session) PrepareBinding(providerName, model string) (ModelBinding, bool, error) {
	factory := s.bindingFactorySnapshot()
	if factory == nil {
		return ModelBinding{}, false, nil
	}
	binding, err := factory(providerName, model)
	return binding, true, err
}

func (s *Session) publishBindingLocked(binding ModelBinding) ModelBinding {
	old := s.binding
	held := s.reasoningEffort
	s.binding = binding
	// The /effort choice belonged to the binding being replaced. The new model
	// may not offer that level at all, so the choice dies here and the new
	// model's configured default takes over.
	//
	// Republishing the same provider/model is not a model change, and the
	// picker's active row states the level in force: committing that row must
	// leave the dial where the row said it was. renameModelLocked and
	// RenameModel already decide "not a rename" the same way.
	//
	// The declared-set clause is what makes that safe. A same-name publication
	// can still carry a different profile (saved-session restore, a binding
	// factory, an edited catalog), and a held level the incoming profile does
	// not declare would ride out on that profile's dialect, which is exactly
	// what the reset exists to prevent.
	if !sameSelection(old, binding) || !slices.Contains(binding.Profile.ReasoningEfforts, held) {
		s.reasoningEffort = ""
	}
	s.Completer = binding.Completer
	s.Dispatcher = binding.Dispatcher
	// The advertised surface and the dispatcher that must execute it are one
	// publication. Skipping this leaves s.Tools advertising a tool the live
	// dispatcher never registered (INV-CE-05-A).
	if binding.Registry != nil {
		s.Tools = binding.Registry
	}
	s.model = binding.Model
	s.MaxContextTokens = binding.PromptBudgetTokens
	s.rejectedSavedModel = nil
	return old
}

// sameSelection reports whether two generations name the same model on the
// same provider. Provider is part of the identity: the same model name served
// by a different provider declares its own reasoning surface.
func sameSelection(old, next ModelBinding) bool {
	return old.ProviderName == next.ProviderName && old.Model == next.Model
}

func (s *Session) bindingAllowsLocked(providerName, model string) bool {
	if len(s.catalog) > 0 {
		for _, group := range s.catalog {
			if group.Provider != providerName || !group.Selectable {
				continue
			}
			for _, profile := range group.Models {
				if profile.Name == model {
					return true
				}
			}
			return false
		}
		return false
	}
	if providerName == s.binding.ProviderName && len(s.allowedModels) > 0 {
		return slices.Contains(s.allowedModels, model)
	}
	if providerName == s.binding.ProviderName && len(s.allowedModels) == 0 {
		return model == s.binding.Model
	}
	return true
}

// captureBindingLocked returns the EFFECTIVE binding for one turn: the
// published generation with the user's /effort choice folded into
// Profile.Reasoning.
//
// Folding here rather than threading the override separately is what keeps
// every request path from plan 37 unchanged. Those paths already capture a
// binding under this lock and read Profile.Reasoning from it, so one fold
// reaches all of them and the effort cannot change mid-turn. A separately
// threaded override would be a second value with its own chance to drift.
//
// The fold lands on the returned COPY only. Writing it back to s.binding would
// make the override indistinguishable from configuration, and the next clear
// would have nothing to restore.
func (s *Session) captureBindingLocked() ModelBinding {
	if s.binding.Completer == nil && s.Completer != nil {
		s.binding.Completer = s.Completer
	}
	if s.binding.Dispatcher == nil && s.Dispatcher != nil {
		s.binding.Dispatcher = s.Dispatcher
	}
	if s.model != "" && s.model != s.binding.Model {
		s.binding.Model = s.model
	}
	binding := s.binding
	binding.Profile.Reasoning = s.effectiveReasoningLocked()
	return binding
}
