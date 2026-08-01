package chat

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

// ModelBinding is one immutable provider/model/backend generation.
type ModelBinding struct {
	ProviderName          string
	Model                 string
	Completer             provider.Completer
	Dispatcher            *runtime.Dispatcher
	SkillRegistry         *skills.Registry
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
		SessionID:          runtime.NewSessionID(),
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
func (s *Session) CurrentBinding() ModelBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureBindingLocked()
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
	current := s.binding.Dispatcher
	guard := s.switchGuard
	s.mu.Unlock()
	if guard != nil {
		if err := guard(); err != nil {
			closeUnpublishedDispatcher(binding.Dispatcher, current)
			return err
		}
	}
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
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	s.mu.Unlock()
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	return nil
}

func closeUnpublishedDispatcher(candidate, current *runtime.Dispatcher) {
	if candidate != nil && candidate != current {
		candidate.Close()
	}
}

// SetPromptBudget applies the per-session prompt cap. Zero clears it and
// recomputes the selected model's configured effective capacity.
func (s *Session) SetPromptBudget(requested int) error {
	if requested < 0 {
		return fmt.Errorf("prompt budget must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := promptBudget(s.binding.Profile, s.MaxTokens, s.operatorPromptCap, 0)
	if requested > base {
		return fmt.Errorf("prompt budget exceeds selected model capacity")
	}
	s.requestedPromptCap = requested
	s.binding.RequestedPromptTokens = requested
	s.binding.PromptBudgetTokens = promptBudget(s.binding.Profile, s.MaxTokens, s.operatorPromptCap, requested)
	s.MaxContextTokens = s.binding.PromptBudgetTokens
	s.invalidateLocked()
	return nil
}

// PromptBudget returns the selected model's effective prompt capacity.
func (s *Session) PromptBudget() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.PromptBudgetTokens
}

// SetMaxSteps applies the per-session interactive step limit safely while a
// turn may be taking a snapshot of its options. Zero means unlimited.
func (s *Session) SetMaxSteps(steps int) error {
	if steps < 0 {
		return fmt.Errorf("max steps must not be negative")
	}
	s.mu.Lock()
	s.MaxSteps = steps
	s.invalidateLocked()
	s.mu.Unlock()
	return nil
}

// MaxStepsValue returns the current interactive step limit safely.
func (s *Session) MaxStepsValue() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.MaxSteps
}

// PromptBudgetFor computes the effective prompt capacity for a candidate
// profile while retaining this session's operator and manual caps.
func (s *Session) PromptBudgetFor(profile config.ModelSpec) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return promptBudget(profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
}

func promptBudget(profile config.ModelSpec, maxTokens *int, operatorCap, requested int) int {
	return config.EffectivePromptTokens(profile, maxTokens, operatorCap, requested)
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

// SetDispatcher attaches the startup dispatcher to the current binding
// generation. This keeps the initial generation subject to the same lifecycle
// boundary as every later model switch.
func (s *Session) SetDispatcher(dispatcher *runtime.Dispatcher) {
	s.mu.Lock()
	old := s.binding.Dispatcher
	s.binding.Dispatcher = dispatcher
	s.Dispatcher = dispatcher
	s.mu.Unlock()
	if old != nil && old != dispatcher {
		old.Close()
	}
}

// SetBindingSkillRegistry attaches the startup skill registry to the current
// immutable generation. Later model switches publish their registry through
// ModelBinding, so callers never observe a dispatcher/catalog mismatch.
func (s *Session) SetBindingSkillRegistry(registry *skills.Registry) {
	s.mu.Lock()
	s.binding.SkillRegistry = registry
	s.mu.Unlock()
}

func (s *Session) publishBindingLocked(binding ModelBinding) ModelBinding {
	old := s.binding
	s.binding = binding
	s.Completer = binding.Completer
	s.Dispatcher = binding.Dispatcher
	s.model = binding.Model
	s.MaxContextTokens = binding.PromptBudgetTokens
	s.rejectedSavedModel = nil
	return old
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
	return s.binding
}

// SelectModel changes the selected model when it is safe and permitted by the
// session's immutable provider policy.
func (s *Session) SelectModel(name string) bool {
	name, err := config.NormalizeModelName(name)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurns > 0 || s.switching {
		return false
	}
	if len(s.allowedModels) > 0 && !slices.Contains(s.allowedModels, name) {
		return false
	}
	s.model = name
	s.binding.Model = name
	if s.binding.ModelGeneration == 0 {
		s.binding.ModelGeneration = 1
	} else {
		s.binding.ModelGeneration++
	}
	s.invalidateLocked()
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
		s.model = normalized
		s.binding.Model = normalized
		return
	}
	saved = strings.TrimSpace(saved)
	s.rejectedSavedModel = &saved
}
