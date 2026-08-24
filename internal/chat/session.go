// Package chat implements multi-turn sessions (plain chat and agent).
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Session holds conversation history and a completer.
type Session struct {
	Completer          provider.Completer
	model              string
	allowedModels      []string
	rejectedSavedModel *string
	SystemPrompt       string
	// BaseSystemPrompt is the memory-block-free prompt (plan 77, E3). The
	// core-memory block never enters the system prompt - it rides in a
	// separate user-role message (setMemoryMessageLocked) - so SystemPrompt
	// and BaseSystemPrompt are always equal. AgentSettings still returns
	// BaseSystemPrompt so surface callers keep a guaranteed block-free base
	// (the AR-1/AR-2 duplication hazard stays structurally impossible).
	BaseSystemPrompt string
	// memoryContext is the rendered core-memory frame carried as a
	// user-role message after the system message (setMemoryMessageLocked).
	// It never enters SystemPrompt: a byte-stable system message is what
	// keeps a memory promotion from invalidating the provider's cached
	// prefix. Stored so /clear (resetSystem) re-seeds it. Guarded by mu.
	memoryContext string
	Temperature   *float64
	MaxTokens     *int
	Messages      []provider.Message
	Tools         *tools.Registry
	// UseTools enables the agent loop when Tools is set.
	UseTools bool
	// Dispatcher is the runtime dispatcher for tool, skill, and subagent execution.
	// When set, it is passed to the agent loop for tool execution. If nil,
	// the agent loop creates a default tool-only dispatcher.
	Dispatcher *runtime.Dispatcher
	// SessionID is an unguessable principal stable for this session's lifetime.
	SessionID string
	MaxSteps  int
	// MaxToolResultChars caps each tool result stored in agent-loop history,
	// in bytes. 0 means uncapped (per-tool budgets are the bound). Set from
	// [tools] max_tool_result_bytes by NewSession.
	MaxToolResultChars int
	// BatchResultBudgetBytes bounds what one tool batch adds to history across
	// all its parallel calls. 0 is off, -1 derives from the prompt budget. Set
	// from [tools] batch_result_budget_bytes by NewSession.
	BatchResultBudgetBytes int
	// RefOnlyTools names tools whose results are always spooled as references.
	RefOnlyTools []string
	// RemainderSpool stores truncated tool-result bodies for read_output.
	// Set from the session dispatcher registration so notices and reads share
	// one grant domain. Nil omits refs from truncation notices.
	RemainderSpool *remainder.Spool
	// MaxContextTokens sets the approximate token limit for pruning.
	// 0 means use default (75% of typical model context window).
	MaxContextTokens int
	// Calibration is the rolling EWMA correction ratio carried across turns.
	// Read into every agent turn snapshot; the zero value is safe (no
	// correction).
	Calibration contextmgr.Calibration
	// ApprovalGate is the synchronous user-approval bridge for tool calls.
	ApprovalGate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult
	// ApprovalStanding is the per-session "always" cache consulted before ApprovalGate.
	ApprovalStanding *sdkadapter.ApprovalStanding
	// ApprovalPolicy controls tool execution approval policy ("write-only", "auto" / "never", "always").
	ApprovalPolicy string
	// BaseApprovalPolicy records the initial configured approval policy before dynamic runtime overrides.
	BaseApprovalPolicy string
	// OnAgentEvent optional tool/step tracing.
	OnAgentEvent func(agent.Event)
	// EventBus optional extensible event delivery (TUI UIAdapter, etc.).
	// When set, the agent loop dual-publishes agent events onto this bus.
	EventBus *events.Bus
	// ToolTimeout is the default per-tool budget for tools that do not
	// declare Capability.Timeout. Zero means agent.DefaultToolTimeout (60s).
	// Long tools (run_command, dispatch_tasks, delegate) still extend via
	// Capability.Timeout regardless of this value.
	ToolTimeout time.Duration
	// SessionDir is the directory where sessions are persisted
	// (e.g., <workspace>/.mivia/sessions/). When set, enables
	// save/load/list/delete operations and auto-save on exit.
	SessionDir string
	// mu protects concurrent mutations to Messages, model, and turnID.
	// All exported methods that read or write these fields must
	// hold mu (Lock for writes, RLock for reads). Save/Load use the
	// lock-and-copy pattern so I/O happens without the lock while
	// the snapshot is safe. TUI code must use MessagesCopy() instead
	// of reading Messages directly to avoid data races.
	mu sync.RWMutex
	// binding is the mutex-owned provider/model/backend generation. Public
	// Completer and Dispatcher remain compatibility mirrors for older callers;
	// turn code captures binding instead of reading those fields after unlock.
	binding ModelBinding
	// eventIdentity builds a validated snapshot for each turn's event stream.
	eventIdentity func(uint64) *events.Identity
	activeTurns   int
	switching     bool
	// loading is held for the whole of Session.Load, which is a surface
	// mutation like any other: it replaces history, advances turnID and
	// republishes the tool surface from the persisted admitted set. It blocks
	// new turns exactly as switching does, but NOT surface publication, because
	// the load performs one itself through the host's widener.
	loading                bool
	agentSurfaceGeneration uint64
	catalog                []config.ProviderModelGroup
	bindingFactory         func(providerName, model string) (ModelBinding, error)
	switchGuard            func() error
	// admittedTools, pendingAdmission and the admission counters are the
	// deferred-tool-loading state for the CURRENT agent binding (plan
	// tools/05). ResetAdmissions clears them on an /agent switch.
	// advertisedToolSpecs is the binding-lifetime pinned tools[] array (plan
	// tools-advertising/01). Only PublishAgentSurface (attach / /agent /
	// /model) writes it; admission publication (TryPublishAgentSurface)
	// changes execution authority (Tools, Dispatcher) only, never this field,
	// so a provider's implicit prompt-cache prefix survives a mid-turn
	// load_tools admission.
	advertisedToolSpecs   []provider.ToolSpec
	admittedTools         []string
	pendingAdmission      *AdmissionStage
	admissionPublications int
	admissionAttempts     int
	admissionDeferrals    int
	// admissionNoOps counts CONSECUTIVE load_tools calls that asked only for
	// already-loaded or already-staged tools. A genuine stage clears it, and so
	// does a turn boundary: the streak is about a loop inside one turn.
	admissionNoOps int
	// admissionRefunds counts no-op attempts refunded for this binding. The
	// refund budget is per binding, not per streak, so the real ceiling on
	// load_tools calls stays within maxConsecutiveAdmissionNoOps of
	// tools.MaxAdmissionAttempts instead of being multiplied by it.
	admissionRefunds int
	admissionNotes   []string
	admissionAgent   string
	admissionDigest  string
	// admissionDeferralReason names why the last boundary deferred the pending
	// stage, or "" when nothing deferred. The staged-tool denial and the
	// load_tools result announce it, so a staged tool never reads as an
	// unknown tool (DC-9: a status says what happened, not what was asked).
	admissionDeferralReason string
	surfaceWidener          SurfaceWidener
	operatorPromptCap       int
	// requestedPromptCap is the user's /budget choice. PromptBudget() reports
	// only the effective capacity, so a surface wanting to say "your budget was
	// reduced" can reach the same wrong answer the /effort dial once did:
	// PromptBudget() != PromptBudgetFor(profile) is a comparison of derived
	// numbers, and a request that coincides with the model's own capacity reads
	// as no request at all. Ask for an accessor to this field instead.
	requestedPromptCap int
	// reasoningEffort is the user's /effort choice for the CURRENT binding.
	// Empty means "use the model's configured default". It is model-scoped and
	// cleared by every binding change, because an effort chosen for one model
	// is meaningless - and possibly unsupported - on the next.
	reasoningEffort reasoning.Level
	// turnID is incremented at the start of each SendUser turn.
	// Writeback of Messages only applies when the turn is still
	// current, so a cancelled/stale turn cannot overwrite a newer one
	// (force-send / overlapping SendUser).
	turnID uint64
	// operationEpoch and contextRevision fence work that outlives the session
	// lock. Clear, load, model/surface changes advance the session domain;
	// successful autosaves advance the durable domain.
	operationEpoch  uint64
	contextRevision contextstate.Revision
	// liveTurnToken is the operation fence re-captured by the most recent
	// step-boundary admission publication, under the post-publication fence
	// (PublishPendingAdmissionAtStepBoundary). The zero value (IdempotencyKey
	// == "") means no step-boundary publication has happened for the current
	// turn; commitTurnToken then falls back to the turn's own captured token.
	liveTurnToken OperationToken
	// sessionStore is the persistence backend for save/load/list/delete.
	// When nil, persistence operations return errors (graceful degradation).
	sessionStore SessionStore
	// saveManager orchestrates auto-save strategies (per-turn, exit, prune).
	// When nil, SaveAfterTurn and SaveLast are no-ops.
	saveManager *SaveManager
	// turnSaveName is the rolling per-turn snapshot directory used by the
	// unwired fallback path, mirroring SaveManager.turnSaveName. Guarded by mu.
	turnSaveName string
	// contextManager is optional and deliberately separate from legacy
	// SessionStore. When enabled, durable turns use the checkpoint publisher
	// and never fall back to raw JSONL autosave.
	contextManager       *contextmgr.ContextManager
	contextPrincipal     contextstate.Principal
	contextPolicy        contextstate.PolicySnapshot
	contextRedaction     contextstate.RedactionPolicy
	contextStore         contextstate.Store
	contextHead          contextstate.Revision
	contextWorktree      contextstate.WorktreeInstance
	contextWorktreeRoot  string
	contextSessionDir    string
	loadedContextSession bool
	// contextPublishMu serializes context publication with clear and turn
	// snapshot capture. Provider calls remain lock-free; only the durable
	// compare-and-swap and its in-memory adoption are serialized.
	contextPublishMu sync.Mutex
	// prefixIdentity is the cached byte-prefix stability identity, refreshed
	// only at NewSession, SwitchBinding, TryPublishAgentSurface and
	// SetReasoningEffort (INV-68-8); prefixGeneration is the /effort offset
	// folded into it without touching binding.ModelGeneration (gap B13).
	prefixIdentity         PrefixIdentity
	prefixIdentityCaptures uint64
	prefixGeneration       uint64
}

// TurnOptions supplies an invocation-local capability surface. It never
// mutates the session-owned registry or binding, which keeps scoped tools from
// leaking into ordinary or concurrent turns. Cleanup runs after history has
// been scrubbed and committed.
type TurnOptions struct {
	Tools      *tools.Registry
	Dispatcher *runtime.Dispatcher
	Cleanup    func()
}

// MessagesCount returns the number of messages under the read lock.
// Safe for concurrent use with agent goroutines.
func (s *Session) MessagesCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Messages)
}

// LoadedContextSession reports whether the most recent Load adopted a durable
// context session (as opposed to a named chat_sessions snapshot).  Callers use
// this to surface the fork-on-load semantics to the user.
func (s *Session) LoadedContextSession() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedContextSession
}

// MessagesCopy returns a deep copy of all conversation messages under the read lock.
// TUI code must call this instead of reading s.Messages directly to avoid data races.
func (s *Session) MessagesCopy() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]provider.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

// UserTurns counts the conversational turns in the live session: user-role
// messages except the session-owned core-memory frame. It routes through the
// same helper the durable sites use (conversationalTurnCount), so the live
// TUI/CLI turn display never disagrees with the saved-sessions list for the
// same memory-enabled session (review LIVE-TURNS-1).
func (s *Session) UserTurns() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return conversationalTurnCount(s.Messages)
}

// SendUser handles one user turn (plain stream or agent loop).
func (s *Session) SendUser(ctx context.Context, userText string, w io.Writer) (string, error) {
	return s.sendUser(ctx, userText, userText, w, nil)
}

// SendUserWithEvent handles one turn with a turn-local event callback.
func (s *Session) SendUserWithEvent(ctx context.Context, userText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUser(ctx, userText, userText, w, onEvent)
}

// SendUserWithEventAndPersistedText sends userText to the provider but keeps
// persistedText in conversation history. It is for UI-only expansions such as
// slash skills, whose private instruction bodies must not enter snapshots.
func (s *Session) SendUserWithEventAndPersistedText(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUser(ctx, userText, persistedText, w, onEvent)
}

// SendUserWithTurnOptions is the scoped-capability variant used by activated
// skills. Passing nil retains the ordinary session behavior.
func (s *Session) SendUserWithTurnOptions(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event), turn *TurnOptions) (string, error) {
	return s.sendUserWithTurn(ctx, userText, persistedText, w, onEvent, turn)
}

func (s *Session) sendUser(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event)) (string, error) {
	return s.sendUserWithTurn(ctx, userText, persistedText, w, onEvent, nil)
}

func (s *Session) sendUserWithTurn(ctx context.Context, userText, persistedText string, w io.Writer, onEvent func(agent.Event), turn *TurnOptions) (string, error) {
	// Publish the turn's callback on the session for the whole turn.
	// emitContextCompaction reads s.OnAgentEvent, and this callback used to
	// reach the agent loop only, so an automatic compaction on the plain
	// (--no-tools) path emitted its typed record to nobody: no "compaction"
	// NDJSON line for a --json consumer, no TUI banner. The agent path
	// already prefers this same function (captureAgentTurn takes the override
	// when set, else this field), so both paths now resolve to one callback
	// and nothing double-emits.
	if onEvent != nil {
		previous := s.SwapOnAgentEvent(onEvent)
		defer s.SwapOnAgentEvent(previous)
	}
	if s.AgentTurnEnabled() {
		return s.sendAgent(ctx, userText, persistedText, w, onEvent, turn)
	}
	if turn != nil && turn.Cleanup != nil {
		defer turn.Cleanup()
	}
	return s.sendPlain(ctx, userText, persistedText, w)
}

func (s *Session) sendPlain(ctx context.Context, userText, persistedText string, w io.Writer) (string, error) {
	snapshot, done, err := s.beginPlainTurn(userText)
	if err != nil {
		return "", err
	}
	defer done()
	if snapshot.context.manager != nil {
		return s.sendPlainContext(ctx, persistedText, w, snapshot)
	}
	return s.sendPlainLegacy(ctx, persistedText, w, snapshot)
}

func (s *Session) sendAgent(ctx context.Context, userText, persistedText string, w io.Writer, eventOverride func(agent.Event), turn *TurnOptions) (string, error) {
	snapshot, done, err := s.beginAgentTurn(userText, eventOverride)
	if err != nil {
		return "", err
	}
	defer done()
	// Publish any stage an earlier boundary could not at the earliest safe
	// point of this turn, and take the surface the loop must run on. The
	// returned token is re-captured after the start-of-turn publication so the
	// loop's commit runs under the post-publication fence
	// (chat-turnstart-admission-fences-own-turn).
	toolRegistry, turnDispatcher, turnToken, turnMessages := s.surfaceForTurnStart(snapshot, turn)
	snapshot.token = turnToken
	snapshot.messages = turnMessages
	loop := &agent.Loop{
		Completer: snapshot.binding.Completer,
		Tools:     toolRegistry,
		Messages:  snapshot.messages,
		// Seeded from the session so the correction keeps accumulating instead
		// of restarting from zero samples every turn.
		Calibration: snapshot.Calibration,
	}
	if snapshot.toolTimeout <= 0 {
		snapshot.toolTimeout = agent.DefaultToolTimeout
	}
	opts := s.buildAgentTurnOptions(snapshot, userText, w, turnDispatcher, turn)
	reply, err := loop.Run(ctx, userText, opts)

	// A step-boundary publication mid-turn swapped the binding surface and
	// bumped the operation fence (TryPublishAgentSurface -> invalidateLocked);
	// hand the commit the token re-captured under the post-publication fence so
	// this turn is not fenced out of its own history. When no step-boundary
	// publication happened, the fallback (the turn's own token) is unchanged.
	commitToken := s.commitTurnToken(uint64(snapshot.myTurn), snapshot.token)
	// loop.Tools is the post-run registry: after a step-boundary publication it
	// carries the newly admitted tools, so the ephemeral-tool scrub sees them.
	if persistErr := s.finishAgentTurn(ctx, loop, loop.Tools, userText, persistedText, commitToken, turn, snapshot.context, err); persistErr != nil && !errors.Is(persistErr, ErrStaleOperation) {
		return reply, persistErr
	}
	return reply, err
}

func (s *Session) buildAgentTurnOptions(snapshot agentTurnSnapshot, userText string, w io.Writer, turnDispatcher *runtime.Dispatcher, turn *TurnOptions) agent.Options {
	opts := agent.Options{
		Model: snapshot.binding.Model, Temperature: snapshot.temperature, MaxTokens: snapshot.maxTokens,
		Reasoning: config.ModelReasoning(snapshot.binding.Profile),
		MaxSteps:  snapshot.maxSteps, MaxContextTokens: snapshot.contextBudget,
		MaxToolResultChars:     snapshot.maxToolResult,
		BatchResultBudgetBytes: snapshot.batchResultBudget,
		RefOnlyTools:           snapshot.refOnlyTools,
		RemainderSpool:         snapshot.remainderSpool,
		RequestTimeout:         DefaultRequestTimeout,
		ToolTimeout:            snapshot.toolTimeout,
		ParentID:               "session",
		TurnID:                 fmt.Sprintf("turn:%d", snapshot.myTurn), SessionID: snapshot.sessionID,
		ApprovalGate:     snapshot.approvalGate,
		ApprovalStanding: snapshot.approvalStanding,
		ApprovalPolicy:   snapshot.approvalPolicy,
		FinalWriter:      w, OnEvent: snapshot.onEvent, EventBus: snapshot.eventBus, EventIdentity: snapshot.identity,
		RequireFinalText: true,
		// Step 1 has no Surface hook call (applySurfaceHook skips it), so the
		// turn's very first request must already carry the pinned snapshot;
		// safe to read fresh here since an active turn blocks /agent and
		// /model from republishing it mid-turn (BeginSurfaceSwitch).
		AdvertisedToolSpecs: s.AdvertisedToolSpecs(),
	}
	if snapshot.context.manager != nil {
		input := prepareInputForContext(snapshot.messages, snapshot.contextBudget, snapshot.maxTokens, snapshot.binding, snapshot.context.principal, snapshot.context.policy, snapshot.context.worktree)
		input.Revision = snapshot.context.revision
		input.CurrentObjective = userText
		opts.PreparationManager = snapshot.context.manager.PreparationManager
		opts.UsageWriter = snapshot.context.manager.UsageWriter
		opts.PreparationInput = input
		opts.SummaryConfig = agent.SummaryConfig{
			Summarizer: snapshot.context.summarizer,
			Redaction:  snapshot.context.redaction,
		}
	}
	if turnDispatcher != nil {
		opts.Dispatcher = turnDispatcher
	}
	// Mid-turn admission publication (w2a/w2d): a tool staged by load_tools
	// becomes callable from the next step via the loop's Surface hook, and a
	// deferred stage reports the reason instead of the unknown-tool denial.
	s.wireStepBoundaryAdmission(&opts, turn)
	return opts
}

// commitTurnToken and SetRemainderSpool live in session_turn_surface.go.

// surfaceForTurnStart publishes any stage an earlier boundary could not
// (guarded boundary, failed save) at the earliest safe point of this turn: no
// batch is running, so closing the previous dispatcher is safe (R2-1), and the
// returned surface carries the staged tool so it is callable from the first
// step (DC-9: staged tools become callable from the next STEP, and this
// turn-start publication remains the cross-turn path for a stage that no step
// boundary could publish). A stage owned by this not-yet-run turn stays
// deferred for its own boundary (D7). Turns with nothing pending keep the
// snapshot path byte-for-byte.
//
// The returned token is the turn's operation fence RE-CAPTURED after the
// start-of-turn publication. That publication swaps the binding surface and
// bumps the operation fence (TryPublishAgentSurface -> invalidateLocked),
// which would fence this turn's own beginAgentTurn token out of
// commitPreparedTurn (chat-turnstart-admission-fences-own-turn). Re-capturing
// pins the fence to the post-publication epoch/revision/binding and to this
// turn's own id, so the loop's commit runs under the fence it actually
// executes on. When the publication deferred (no bump), the re-capture is a
// no-op re-read. Genuine supersession still refuses: a superseding turn
// advances s.turnID, and sameFence compares TurnID.
func (s *Session) surfaceForTurnStart(snapshot agentTurnSnapshot, turn *TurnOptions) (*tools.Registry, *runtime.Dispatcher, OperationToken, []provider.Message) {
	toolRegistry, turnDispatcher := resolveTurnExecutionSurface(snapshot.toolRegistry, snapshot.binding.Dispatcher, turn)
	if !snapshot.pendingAdmission {
		return toolRegistry, turnDispatcher, snapshot.token, snapshot.messages
	}
	s.PublishPendingAdmissionAtTurnStart()
	// The snapshot predates the start-of-turn publication. Read the live
	// surface once so the loop's registry and dispatcher carry the staged tool
	// and stay in agreement (INV-AG-29); a later mid-turn switch still cannot
	// change what this turn captured.
	s.mu.RLock()
	liveTools := s.Tools
	liveDispatcher := s.binding.Dispatcher
	// The publication can rewrite s.Messages (setMemoryMessageLocked places or
	// replaces the core-memory frame). The snapshot's message clone predates
	// that, so running the loop on it - and later committing the loop's
	// history - would stomp the just-published frame. Re-read the live history
	// under the same lock so the turn runs on, and commits on top of, the
	// post-publication messages.
	liveMessages := cloneContextMessages(s.Messages)
	s.mu.RUnlock()
	toolRegistry, turnDispatcher = resolveTurnExecutionSurface(liveTools, liveDispatcher, turn)
	return toolRegistry, turnDispatcher, s.captureTurnToken(snapshot.myTurn), liveMessages
}

// adoptCalibration copies a finished turn's rolling token calibration back
// into the session so the next turn starts from it.
//
// Deliberately not fenced by the turn's operation token, unlike history: an
// estimate-vs-actual observation stays true even when the turn errored or its
// fence went stale, and discarding it would leave the heuristic uncorrected
// exactly on the long turns that drift most. Concurrent turns each start from
// the same seed, so the one with the most samples is the most informed; the
// count only ever grows on top of what the turn was seeded with.
func (s *Session) adoptCalibration(turnCalibration contextmgr.Calibration) {
	if turnCalibration.Samples == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if turnCalibration.Samples >= s.Calibration.Samples {
		s.Calibration = turnCalibration
	}
}

// runTurnCleanup invokes the optional per-turn cleanup callback.
func (s *Session) runTurnCleanup(turn *TurnOptions) {
	if turn != nil && turn.Cleanup != nil {
		turn.Cleanup()
	}
}
