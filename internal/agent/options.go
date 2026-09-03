package agent

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/usage"
)

// UnadmittedToolResult is returned by Options.UnadmittedToolHandler.
type UnadmittedToolResult struct {
	// Handled is false when the handler has no opinion on this tool name at
	// all (not advertised, a hallucinated name); the caller falls through to
	// its own generic "not available" denial. True for every other case.
	Handled bool
	// Execute, when non-nil, is a tool the host has AUTHORIZED for this call
	// but which is absent from the SDK registry - the deferred-tool case. The
	// loop runs it through the same shim an admitted call uses
	// (RunUnadmittedTool), so every execution contract holds by construction.
	//
	// The host decides; the loop executes. That split is deliberate: approval
	// has to happen before the host charges an admission attempt or stages a
	// publication, so it cannot be deferred to here - and execution has to
	// happen through the shim, so it cannot be done there. Returning the tool
	// is what lets each side own its half.
	//
	// When set, Ran/Failed/Content are ignored: the shim produces them.
	Execute tools.Tool
	// Ran is true when the tool call REACHED THE DISPATCHER and Content is
	// its real result: the caller renders it exactly like an ordinary
	// admitted tool call - no "error: " prefix and no denial framing of its
	// own. False means Content is a human-readable denial reason instead
	// (e.g. staged but could not run synchronously); the caller applies its
	// own "error: " framing as before.
	//
	// Ran does NOT mean the call succeeded - see Failed. It used to, and
	// that is exactly why an executed-then-errored call had to report
	// Ran=false to avoid being rendered green, which made the caller tell
	// the model the call never happened and to retry it. The two questions
	// - did it run, did it succeed - are now two fields, because collapsing
	// them left no truthful answer for a call that ran and failed.
	Ran bool
	// Failed marks a Ran call that did not succeed: the tool errored, or a
	// PreToolUse hook blocked it, or approval refused it. The caller records
	// a FAILED outcome for it, which is what stops the TUI, the NDJSON
	// status mapping and any remote viewer from showing a refusal or a
	// broken tool as a completed call. Meaningless when Ran is false, where
	// the caller already records a failure.
	Failed bool
	// Content is the tool's real result (Ran) or the denial text (!Ran).
	Content string
	// HookRuns are the lifecycle hooks that executed for this call, for the
	// OPERATOR's view. Set on the Ran path AND on a !Ran path whose cause
	// was a PreToolUse block - the case an operator most needs to see, since
	// it is the run that stopped the call. It must be nil for a dedup-served
	// duplicate: a duplicate is answered with the OWNER's runs (DC-9), which
	// did not execute for THIS call, so reporting them would show a hook
	// firing that never fired here. Nil when the handler never reached the
	// dispatcher at all.
	HookRuns []runtime.HookRun
	// HookContext is the advisory text lifecycle hooks produced for this
	// call, for the MODEL. The caller frames it through appendHookContext,
	// so it gets the same delimiting and tag-neutralization the ordinary
	// dispatcherShim.Run path applies. Unlike HookRuns it IS set for a
	// dedup-served duplicate: DC-9 answers a duplicate with the owner's
	// post-hook Result, and the shim appends that context too. Set but
	// currently unused on a !Ran (denial) result: the denial text is left
	// unchanged until a PreToolUse block's own advisory context is threaded
	// too (see runDeferredToolNow's doc comment).
	HookContext string
}

// Options is one agent turn's immutable configuration. Every field is read,
// never written, by the loop, so a turn keeps the settings it started with
// even if the session changes underneath it.

type Options struct {
	Model       string
	Temperature *float64
	MaxTokens   *int
	// AdvertisedToolSpecs, when non-nil, is the tools[] array Run serializes
	// on every step of this turn - the host's pinned, binding-lifetime
	// snapshot (plan tools-advertising/01), computed once from the session's
	// admissible union and byte-identical across turns and admissions. Run
	// falls back to Tools.OpenAITools() when nil, which is today's behavior:
	// subagent and workflow-engine loops that never set this field are
	// unaffected.
	AdvertisedToolSpecs []provider.ToolSpec
	// Reasoning is the selected model's reasoning dial, carried onto every
	// request this loop makes. Its zero value sends nothing.
	Reasoning  reasoning.Setting
	MaxSteps   int
	WorkLimits runtime.WorkLimits
	// PreserveWorkLimits keeps cumulative reservations across a corrective
	// re-entry of the same task invocation.
	PreserveWorkLimits    bool
	DisableProviderReplay bool
	// WireStreamTransport asks the provider layer to carry every
	// non-streaming turn of this loop on the SSE endpoint (stream:true on the
	// wire) while keeping the non-stream contract: the full response is
	// assembled before it comes back. Turns that stream to a FinalWriter are
	// unaffected. The provider layer bounds each wire-stream attempt with a
	// content-idle watchdog, so a keepalive trickle can no longer hold a
	// nested turn open. See provider.Request.StreamTransport.
	WireStreamTransport bool
	// MaxContextTokens sets the approximate token limit for the prompt context.
	// Pruning is hysteretic, mirroring contextmgr.Plan: history is left
	// untouched below 80% of the budget, and once that trigger is crossed old
	// turns are dropped (keeping system prompt and recent turns) down to ~50%,
	// so one provider prompt-cache miss buys many cache hits before the next.
	// 0 or negative means no pruning.
	MaxContextTokens int
	// MaxToolResultChars caps each tool result stored in conversation history,
	// in BYTES despite the name (it bounds len() of the UTF-8 body; see
	// capToolResult). This prevents a single large output (e.g. read_file of
	// 256KB) from exceeding the context budget. 0 means no cap (use full
	// result); per-tool Capability.MaxResultBytes budgets still apply. To bound
	// one oversized result against the prompt budget while keeping full results
	// the default, set BatchResultBudgetBytes < 0 (derived batch budget)
	// instead.
	MaxToolResultChars int
	// BatchResultBudgetBytes bounds the bytes ONE tool batch may add to
	// history, across all its parallel calls together. Per-call caps cannot
	// see each other, so N calls each honestly under its own cap still blow
	// the context when they land in the same step; this is the only bound that
	// sees the batch as a whole.
	//
	// 0 (the default) disables the mechanism entirely - shapeBatch is not
	// invoked and the append path is byte-identical to having no budget.
	// Negative derives it from MaxContextTokens (inert when that is unset).
	// Positive is the literal byte budget. Scope is one runToolBatch: nothing
	// is charged across compaction boundaries, where cross-batch growth is
	// already compaction's job.
	BatchResultBudgetBytes int
	// RefOnlyTools lists tool names whose results are never inlined into the
	// model context when they exceed BatchDegradeFloorBytes; instead the whole
	// body is spooled to RemainderSpool and the notice names a remainder ref
	// (read_output) that the model can fetch. Empty/absent names keep normal
	// degrade behavior.
	RefOnlyTools         []string
	MaxToolCallsPerBatch int
	MaxConcurrentTools   int
	ToolTimeout          time.Duration
	// ToolRunTimeout is the SDK tool-registry's registry-wide run-timeout
	// backstop for tools that declare no Capability.Timeout (the [tools]
	// tool_run_timeout_seconds knob). <= 0 (the default) maps to the SDK's
	// TimeoutNone: no registry-wide cap, because the dispatcher shim
	// already arms every call's Capability.Timeout / ToolTimeout as a real
	// deadline and the SDK backstop must never be tighter than those
	// declared budgets. Positive is the literal bound.
	ToolRunTimeout time.Duration
	RequestTimeout time.Duration
	ParentID       string
	TurnID         string
	SessionID      string
	Role           string
	Depth          int
	// Step is the loop's 1-based model-step index, stamped per step on the
	// loop's own Options copy before tool calls are dispatched (plan:
	// step-scoped tool dedup). 0 means unset/legacy.
	Step       int
	Budget     int
	Dispatcher *runtime.Dispatcher
	// StagedToolMessage returns the denial message for a tool call to a name
	// staged for loading (load_tools) but not yet published to the live tool
	// surface, plus true. The loop calls it only when the name is absent from
	// its registry. The returned message announces why publication is pending;
	// nil means no check and the generic denial message is used. It must be
	// safe for concurrent calls.
	StagedToolMessage func(name string) (string, bool)
	// UnadmittedToolHandler is checked when a call names a tool absent from
	// the live registry AND StagedToolMessage found no pending stage for it.
	// It lets the host recognize a tool that IS advertised (plan
	// tools-advertising/01: the wire tools[] array now includes every
	// deferred candidate, not just admitted ones) but not yet admitted for
	// execution: auto-stage it for native publication at the next step
	// boundary (so later calls in the turn need no special handling), AND
	// serve THIS call synchronously against the full authorized tool set
	// when possible, so the model never sees an error for a call it already
	// made correctly. Handled=false means the name is not recognized at all
	// (a hallucinated tool) and the caller falls through to the generic
	// denial. Nil disables the check. Must be safe for concurrent calls.
	UnadmittedToolHandler func(ctx context.Context, name string, args json.RawMessage) UnadmittedToolResult
	// ApprovalGate is the synchronous user-approval bridge for tool calls.
	// It is invoked by executeToolTask and by the SDK-path approval wrapper
	// before Dispatcher.Invoke for any tool whose capability.Class >=
	// tools.ExecutionWrite; tools of class ExecutionRead or Unclassified
	// skip the call. The function MUST be safe to call concurrently from
	// multiple goroutines (parallel tool batches). A nil gate is equivalent
	// to "approve every call": the loop runs as if there were no approval
	// surface at all, matching pre-Phase-4 behavior.
	ApprovalGate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult
	// ApprovalStanding is consulted BEFORE ApprovalGate to honor "always"
	// decisions ("a always" / "D deny always"). It is keyed on tool name
	// and carries a verdict (approved or denied) plus a class tag so the
	// same call short-circuits the gate for the rest of the session.
	// Nil is safe: every call falls through to ApprovalGate. The same
	// instance backs the SDK-path wrapper so a "always" decision persists
	// across legacy and SDK turns within one session.
	ApprovalStanding *sdkadapter.ApprovalStanding
	// ApprovalPolicy controls tool execution approval policy ("write-only", "auto" / "never" [yolo], "always").
	ApprovalPolicy string
	// RemainderSpool, when non-nil, stores truncated tool-result bodies under
	// content refs so the model can page them via read_output. Nil means
	// truncation notices omit refs (legacy plain notices).
	RemainderSpool *remainder.Spool
	OnEvent        func(Event)
	EventBus       *events.Bus // publishes agent events to extensible delivery
	// EventIdentity is a validated public identity snapshot for this turn.
	EventIdentity *events.Identity
	// UsageWriter, when non-nil, durably records token/cache/compaction usage
	// measurements alongside the existing EventBus publish. Nil keeps usage
	// events exactly as ephemeral as they are today (subagent/workflow-engine
	// loops that never set this field are unaffected).
	UsageWriter usage.UsageWriter
	FinalWriter io.Writer
	// RequireFinalText fails a turn that produced no assistant text anywhere
	// instead of reporting an empty success. Interactive surfaces set it: a turn
	// that renders as "done" with no answer is indistinguishable from the agent
	// stopping for no reason. Sub-agents leave it false, because buildResult
	// discards a task's output whenever its error is non-nil, and a task that
	// did its work through tools and then stopped without prose did succeed.
	RequireFinalText bool
	// MaxUnactedContinuations bounds how many times one turn may be continued
	// after it announced work and then ended without calling a single tool
	// (unacted_turn.go). Zero, the default, disables the mechanism entirely:
	// whether a model narrates instead of acting is a property of the model,
	// so an operator opts in per deployment ([chat]
	// max_unacted_continuations). A continuation only ever fires for a turn
	// that ran NO tool, so it can never repeat work that already happened.
	//
	// Root chat turns only. internal/subagents deliberately leaves this
	// zero: a sub-agent's prose is not delivered to a human who would
	// otherwise have to re-prompt, its parent already re-reads the result,
	// and a nested loop that continued itself would multiply the fan-out
	// budget the orchestrator sized. The knob lives under [chat] for the
	// same reason.
	MaxUnactedContinuations int
	// PreparationManager is an optional root-owned preparation capability. It
	// has no checkpoint publisher and is therefore safe to pass to nested loops.
	PreparationManager contextmgr.PreparationManager
	PreparationInput   contextmgr.PrepareInput
	// SummaryConfig wires the optional LLM summarizer into the request path. A
	// nil Summarizer keeps the loop structural-only: no summary provider call,
	// no injected message, byte-identical requests. Redaction is the host's
	// compiled redaction policy applied to summary input and output through
	// the summary validators.
	SummaryConfig SummaryConfig
	// BeforeStep, when set, is called on the loop goroutine at the top of each
	// step before history pruning and request build (plan 53.03). Returned
	// messages are appended to the loop history. Nil is a no-op.
	BeforeStep func() []provider.Message
	// ObserveRequestHistory, when set, is called on the loop goroutine at each
	// step with the prepared history about to be sent, immediately after
	// pruning and compaction. It is an OBSERVER: the slice must not be
	// retained or mutated, and the hook must not block the step.
	//
	// It exists because the loop's carried history is only written back to the
	// host when the turn ends, so a host that wants to describe the context
	// mid-turn has nothing to describe until then. This is the one place the
	// exact billed message list is in hand on every step. Nil is a no-op.
	ObserveRequestHistory func([]provider.Message)
	// InterruptCh, when non-nil, resolves the channel a parent can signal to
	// softly interrupt the in-flight LLM call (plan 54). It is re-read once
	// per LLM call. Nil disables the signal path. A steer never cancels a tool
	// batch: only the LLM-scoped context is cancelable.
	InterruptCh func() <-chan struct{}
	// MailboxPending, when non-nil, reports whether ANY message is waiting in
	// the mailbox. The watchdog path cancels only when it returns true, so a
	// stale signal after a drain can never cancel a call. The interrupt-signal
	// path uses the stricter MailboxPendingInterrupt, so a stale signal paired
	// with a later non-interrupt message is never a cancel.
	MailboxPending func() bool
	// MailboxPendingInterrupt, when non-nil, reports whether an
	// Interrupt-flagged steer is queued. The watcher's signal branch cancels
	// only when it returns true; the watchdog branch keeps gating on
	// MailboxPending (any pending message bounds non-urgent steer latency).
	// Nil disables the signal gate.
	MailboxPendingInterrupt func() bool
	// WatchdogInterval bounds steer latency when no interrupt signal is wired:
	// with a steer pending, the in-flight LLM call is softly interrupted at
	// most this often. 0 disables the watchdog.
	WatchdogInterval time.Duration
	// SoftInterruptCooldown caps soft-interrupt frequency across calls: at
	// most one interrupt per window. 0 disables the cooldown (tests). The
	// production 5s default lives in the subagents wiring, NOT here.
	SoftInterruptCooldown time.Duration
	// Surface, when non-nil, is invoked by Loop.Run at the top of EVERY step
	// iteration (before runStep, hence before BeforeStep inside prepareStep) to
	// fetch that step's host surface. Non-nil fields of the returned Surface
	// are applied to that step only; nil fields leave the loop's own state
	// unchanged. The host must supply Registry, Dispatcher, and ToolSpecs from
	// one consistent read so the registry/dispatcher/spec agreement invariant
	// (M3) holds for the step. Nil is a no-op.
	Surface func() Surface
	// OnToolCancelReady, when non-nil, is invoked exactly once per SDK-backed
	// run - as soon as the run's per-turn cancel registry exists, before any
	// tool call executes - with a ToolCanceler the host can retain past the
	// call that constructed it and invoke later, from any goroutine, to
	// cancel ONE in-flight tool call by its call ID without aborting the
	// rest of the turn or any concurrent sibling call. The turn's internal
	// state (sdkTurnState) is not exported; this is the minimal seam a host
	// needs to reach it. A legacy (non-SDK) run never calls this hook, so a
	// host relying on it alone sees no cancel capability on that backend -
	// treat a nil ToolCanceler, or one that always returns false, as "not
	// supported here" rather than an error.
	OnToolCancelReady func(ToolCanceler)
}

// ToolCanceler cancels one in-flight tool call by its call ID. It returns
// whether a matching in-flight call was found; a miss (already finished,
// unknown ID, or nothing in flight) is a no-op that returns false. Safe to
// call from any goroutine and more than once for the same ID.
type ToolCanceler func(callID string) bool

// Surface is one step's host-supplied tool surface: the registry the loop
// dispatches against, the runtime dispatcher for per-turn dedup, the tool
// specs published to the model for the step, and the spool for remainder refs.
// Fields come from one consistent host read; a zero field means "keep the
// loop's current value for this step".
type Surface struct {
	Registry       *tools.Registry
	Dispatcher     *runtime.Dispatcher
	ToolSpecs      []provider.ToolSpec
	RemainderSpool *remainder.Spool
}

// SummaryConfig is one turn's immutable summary wiring. It is read, never
// written, by the loop, mirroring Options itself.
type SummaryConfig struct {
	// Summarizer is the captured provider/model/policy binding. Nil disables
	// summary injection entirely.
	Summarizer *contextmgr.Summarizer
	// UnavailableReason names the setup-time failure that prevented a Summarizer
	// from being wired, when Summarizer is nil.
	UnavailableReason string
	// Redaction is the host's compiled redaction policy. It classifies every
	// envelope field and every provider output before anything reaches the
	// wire or storage.
	Redaction contextstate.RedactionPolicy
}

// Approval types live in internal/sdkadapter so the SDK-path wrapper can
// use them without creating an import cycle (internal/sdkadapter already
// imports nothing from internal/agent).
