package agent

import (
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// Options is one agent turn's immutable configuration. Every field is read,
// never written, by the loop, so a turn keeps the settings it started with
// even if the session changes underneath it.

type Options struct {
	Model       string
	Temperature *float64
	MaxTokens   *int
	// Reasoning is the selected model's reasoning dial, carried onto every
	// request this loop makes. Its zero value sends nothing.
	Reasoning reasoning.Setting
	MaxSteps  int
	// MaxContextTokens sets the approximate token limit for the prompt context.
	// When exceeded, old messages are pruned (keeping system prompt and recent turns).
	// 0 or negative means no pruning.
	MaxContextTokens int
	// MaxToolResultChars caps each tool result stored in conversation history,
	// in BYTES despite the name (it bounds len() of the UTF-8 body; see
	// capToolResult). This prevents a single large output (e.g. read_file of
	// 256KB) from exceeding the context budget. 0 means no cap (use full
	// result); per-tool Capability.MaxResultBytes budgets still apply.
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
	MaxToolCallsPerBatch   int
	MaxConcurrentTools     int
	ToolTimeout            time.Duration
	RequestTimeout         time.Duration
	ParentID               string
	TurnID                 string
	SessionID              string
	Role                   string
	Depth                  int
	Budget                 int
	Dispatcher             *runtime.Dispatcher
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
	// BeforeStep, when set, is called on the loop goroutine at the top of each
	// step before history pruning and request build (plan 53.03). Returned
	// messages are appended to the loop history. Nil is a no-op.
	BeforeStep func() []provider.Message
}
