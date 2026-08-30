package chat

import (
	"errors"
	"fmt"
	"os"
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

// WarnUnknownContextWindow writes a warning when a model's context window is unknown.
var WarnUnknownContextWindow = func(model string) {
	fmt.Fprintf(os.Stderr, "warning: model %q context window is unknown; defaulting to %d tokens\n", model, config.UnknownContextWindowTokens)
}

func (s *Session) checkWarnUnknownModelLocked(model string, isFallback bool) bool {
	if !isFallback {
		return false
	}
	if s.warnedUnknownModels == nil {
		s.warnedUnknownModels = make(map[string]struct{})
	}
	if _, seen := s.warnedUnknownModels[model]; seen {
		return false
	}
	s.warnedUnknownModels[model] = struct{}{}
	return true
}

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
	Registry *tools.Registry
	// AdvertisedToolSpecs is this generation's pinned tools[] array (plan
	// tools-advertising/01), published onto the session alongside Registry.
	// Nil Registry means "session surface untouched" (a generation clone with
	// no captured agent surface); AdvertisedToolSpecs follows the same rule.
	AdvertisedToolSpecs   []provider.ToolSpec
	Profile               config.ModelSpec
	RequestedPromptTokens int
	PromptBudgetTokens    int
	// FallbackProfile indicates the model's profile was synthesized because
	// the model's context window was undeclared in the configured catalog.
	FallbackProfile bool
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
	// DefaultRequestTimeout is the fallback per-request deadline for an agent
	// turn when Session.RequestTimeout is zero (a session built from a
	// hand-built Resolved with no [chat] request_timeout_seconds resolution).
	// It is defined from config.DefaultChatRequestTimeoutSeconds so the two
	// values cannot drift; NewSession normally carries the resolved value in.
	DefaultRequestTimeout = config.DefaultChatRequestTimeoutSeconds * time.Second

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

// batchResultBudget resolves the [tools] batch_result_budget_bytes knob with
// the same nil-safe pattern as promptCap: an absent key (nil) falls back to
// the derived budget sentinel, not to "off" (0). Explicit 0 stays off;
// config.Load has already normalized negatives to the derived sentinel.
func batchResultBudget(res *config.Resolved) int {
	if res.Tools.BatchResultBudgetBytes == nil {
		return config.BatchResultBudgetDerived
	}
	return *res.Tools.BatchResultBudgetBytes
}

// NewSession builds a session from resolved config and completer.
func NewSession(res *config.Resolved, c provider.Completer) *Session {
	providerName := res.ProviderName
	if providerName == "" && c != nil {
		providerName = c.Name()
	}
	profile, found := config.ResolveModelProfile(res.ModelProfiles, res.Model)
	operatorCap := 0
	if res.MaxPromptTokens != nil {
		operatorCap = *res.MaxPromptTokens
	}
	ctxBudget := promptBudget(profile, res.MaxTokens, operatorCap, 0)
	s := &Session{
		Completer:     c,
		model:         res.Model,
		allowedModels: slices.Clone(res.Models),
		SystemPrompt:  res.SystemPrompt,
		// res.SystemPrompt at construction time has no memory block composed
		// in yet (plan 77, E3) - it's the same value for both fields until
		// the first PublishAgentSurface/SetAgentSettings call. Without this,
		// AgentSettings() would return "" here instead of the real initial
		// prompt, since it reads BaseSystemPrompt, not SystemPrompt.
		BaseSystemPrompt:        res.SystemPrompt,
		Temperature:             res.Temperature,
		MaxTokens:               res.MaxTokens,
		MaxSteps:                resolvedMaxSteps(res), // /steps overrides (0 = unlimited)
		MaxUnactedContinuations: res.MaxUnactedContinuations,
		MaxContextTokens:        ctxBudget,
		// 0 = uncapped; config.Load already normalized negatives and enforced
		// the 1024-byte floor for positive values.
		MaxToolResultChars: res.Tools.MaxToolResultBytes,
		// 0 = no registry-wide SDK run backstop; config.Load already
		// normalized negatives to 0.
		ToolRunTimeout: config.SaturatingSeconds(res.Tools.ToolRunTimeoutSec),
		// Zero (hand-built Resolved) falls back to DefaultRequestTimeout in
		// buildAgentTurnOptions.
		RequestTimeout: res.ChatRequestTimeout,
		// 0 = off; config.Load already normalized negatives to the derived
		// sentinel and rejected positive values under the degrade floor.
		BatchResultBudgetBytes: batchResultBudget(res),
		RefOnlyTools:           slices.Clone(res.Tools.RefOnlyTools),
		SessionID:              runtime.NewSessionID(),
	}
	s.agentSurfaceGeneration = 1
	s.operatorPromptCap = operatorCap
	s.catalog = res.ModelCatalog()
	s.binding = ModelBinding{ProviderName: providerName, Model: res.Model, Completer: c, Profile: profile, PromptBudgetTokens: ctxBudget, ModelGeneration: 1, FallbackProfile: !found}
	s.resetSystem()
	// NewSession is one of the four identity-capture triggers (INV-68-8): the
	// cache must be primed before any switch or publication can compare
	// against it.
	s.mu.Lock()
	warn := s.checkWarnUnknownModelLocked(profile.Name, !found)
	s.refreshPrefixIdentityLocked()
	s.mu.Unlock()
	if warn && WarnUnknownContextWindow != nil {
		WarnUnknownContextWindow(profile.Name)
	}
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
	binding, current, err := s.prepareSwitchBinding(binding)
	if err != nil {
		return err
	}
	s.contextPublishMu.Lock()
	defer s.contextPublishMu.Unlock()
	s.mu.Lock()
	if s.activeTurns > 0 {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model switching is unavailable while work is active"))
	}
	if s.switching {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model switching is unavailable while session surface is changing"))
	}
	if binding.AgentSurfaceGeneration != 0 && binding.AgentSurfaceGeneration != s.agentSurfaceGeneration {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model binding was prepared for an outdated agent surface"))
	}
	if !s.bindingAllowsLocked(binding.ProviderName, binding.Model) {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, fmt.Errorf("model is not configured for provider %s", binding.ProviderName))
	}
	binding.RequestedPromptTokens = s.requestedPromptCap
	binding.PromptBudgetTokens = promptBudget(binding.Profile, s.MaxTokens, s.operatorPromptCap, s.requestedPromptCap)
	if binding.PromptBudgetTokens <= 0 {
		return s.refuseSwitchLocked(binding, s.binding.Dispatcher, errors.New("model has no usable prompt budget"))
	}
	binding.ModelGeneration = s.binding.ModelGeneration + 1
	contextStore := s.contextStore
	contextPrincipal := s.contextPrincipal
	contextWorktree := s.contextWorktree
	contextExpected := s.contextHead
	contextEnabled := s.contextEnabledLocked() && contextStore != nil
	expectedBinding := captureBindingRevision(s.binding)
	newBinding := captureBindingRevision(binding)
	if err := s.advanceBindingIfNeeded(contextEnabled, contextStore, contextPrincipal, contextWorktree, contextExpected, expectedBinding, newBinding, "switch"); err != nil {
		return s.refuseSwitchLocked(binding, current, fmt.Errorf("advance context binding: %w", err))
	}
	// Reasoning replay is provider-scoped: chain-of-thought belongs to the
	// provider that produced it, so a generation that moves to a different
	// provider drops the bytes rather than ship another provider's reasoning
	// to a backend that never produced it (see stripReasoningForProviderSwitch).
	s.stripReasoningForProviderSwitch(binding)
	outgoing := s.prefixIdentity
	old := s.publishBindingLocked(binding)
	s.invalidateLocked()
	if contextEnabled {
		s.contextHead = contextstate.Revision{Session: contextExpected.Session + 1, Durable: contextExpected.Durable + 1, Source: contextExpected.Source}
	}
	// SwitchBinding is one of the four identity-capture triggers (INV-68-8).
	// The incoming identity reflects the newly published binding; when the
	// wire-affecting subset changed, exactly one KindPrefixReset event is
	// built under the lock and published after unlock (INV-68-2).
	incoming := s.capturePrefixIdentityLocked()
	s.prefixIdentity = incoming
	reset := s.buildPrefixResetLocked(outgoing, incoming, false)
	warn := s.checkWarnUnknownModelLocked(binding.Model, binding.FallbackProfile)
	bus := s.EventBus
	s.mu.Unlock()
	if warn && WarnUnknownContextWindow != nil {
		WarnUnknownContextWindow(binding.Model)
	}
	publishPrefixResetEvent(bus, s.SessionID, reset)
	if old.Dispatcher != nil && old.Dispatcher != binding.Dispatcher {
		old.Dispatcher.Close()
	}
	return nil
}

// prepareSwitchBinding validates and normalizes an incoming binding before the
// switch critical section, closing the candidate dispatcher on any refusal. It
// returns the normalized binding and the active dispatcher snapshot captured by
// preflight; the caller must not reuse the candidate after an error.
func (s *Session) prepareSwitchBinding(binding ModelBinding) (ModelBinding, *runtime.Dispatcher, error) {
	name, err := config.NormalizeModelName(binding.Model)
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return binding, nil, err
	}
	if strings.TrimSpace(binding.ProviderName) == "" || binding.Completer == nil {
		closeUnpublishedDispatcher(binding.Dispatcher, nil)
		return binding, nil, fmt.Errorf("incomplete model binding")
	}
	binding.Model = name
	current, err := s.switchPreflight()
	if err != nil {
		closeUnpublishedDispatcher(binding.Dispatcher, current)
		return binding, current, err
	}
	return binding, current, nil
}

// stripReasoningForProviderSwitch drops reasoning_content from the in-memory
// history when a new binding moves the session to a DIFFERENT provider:
// chain-of-thought belongs to the provider that produced it, and replaying one
// provider's CoT to another backend is cross-model contamination. A
// same-provider model change keeps the bytes — both DeepSeek models are
// replay-capable, and stripping there would break the D2 gate. Runs inside the
// SwitchBinding critical section (s.mu held); a concurrent turn cannot start
// mid-strip.
func (s *Session) stripReasoningForProviderSwitch(binding ModelBinding) {
	if binding.ProviderName != s.binding.ProviderName {
		for i := range s.Messages {
			s.Messages[i].ReasoningContent = ""
		}
	}
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

// refuseSwitchLocked refuses a switch whose preconditions fail under the
// session lock: it closes the unpublished candidate (unless it is the active
// dispatcher) and releases the lock. Callers hold s.mu and must return the
// error immediately.
func (s *Session) refuseSwitchLocked(binding ModelBinding, current *runtime.Dispatcher, err error) error {
	s.mu.Unlock()
	closeUnpublishedDispatcher(binding.Dispatcher, current)
	return err
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
		s.advertisedToolSpecs = binding.AdvertisedToolSpecs
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
