package agent

import (
	"context"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

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
	RequestTimeout       time.Duration
	ParentID             string
	TurnID               string
	SessionID            string
	Role                 string
	Depth                int
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
	// execution, auto-stage it for publication at the next step boundary, and
	// return a message explaining the call must be retried after that. False
	// means the name is not recognized at all (a hallucinated tool), and the
	// generic denial message is used. Nil disables the check. Must be safe
	// for concurrent calls.
	UnadmittedToolHandler func(ctx context.Context, name string) (string, bool)
	// RemainderSpool, when non-nil, stores truncated tool-result bodies under
	// content refs so the model can page them via read_output. Nil means
	// truncation notices omit refs (legacy plain notices).
	RemainderSpool *remainder.Spool
	OnEvent        func(Event)
	EventBus       *events.Bus // publishes agent events to extensible delivery
	// EventIdentity is a validated public identity snapshot for this turn.
	EventIdentity *events.Identity
	FinalWriter   io.Writer
	// RequireFinalText fails a turn that produced no assistant text anywhere
	// instead of reporting an empty success. Interactive surfaces set it: a turn
	// that renders as "done" with no answer is indistinguishable from the agent
	// stopping for no reason. Sub-agents leave it false, because buildResult
	// discards a task's output whenever its error is non-nil, and a task that
	// did its work through tools and then stopped without prose did succeed.
	RequireFinalText bool
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
}

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
	// Redaction is the host's compiled redaction policy. It classifies every
	// envelope field and every provider output before anything reaches the
	// wire or storage.
	Redaction contextstate.RedactionPolicy
}
