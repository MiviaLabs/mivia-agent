package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ErrSchemaViolation marks a task that exhausted schema-validation retries.
// Parent envelopes map it to reason "schema_violation" without carrying the
// error text (fixed termination vocabulary).
var ErrSchemaViolation = errors.New("schema_violation")

// MultiStepHandler implements runtime.Handler by creating a mini agent.Loop
// with tool access. Sub-agents never receive delegation or orchestration
// control tools; only the root orchestrator may create or control runs.
type MultiStepHandler struct {
	// Approval supplies the operator's live approval wiring for this run's
	// nested loop: the gate, the policy, and the standing cache.
	//
	// It is a FUNCTION, read once per invocation, and both halves of that
	// matter. The dispatcher is built before the TUI installs a gate, so a
	// value captured at construction is always nil; and the policy changes
	// mid-session through /yolo and the settings screen, so a value captured
	// at the first invocation goes stale.
	//
	// Nil means no wiring, which is what every construction site did before
	// this existed: the nested loop then runs ungated, exactly as it always
	// has. That is a compatibility floor, not a design - a site that leaves
	// this nil lets a delegated call skip an approval the same call would
	// face on the root path.
	Approval func() sdkadapter.ApprovalDeps

	// Completer is the LLM provider used by the sub-agent loop.
	Completer provider.Completer
	// FullRegistry is the parent's complete tool registry.
	// The handler creates a restricted copy (minus delegation tools).
	FullRegistry *tools.Registry
	// Dispatcher is the parent session dispatcher. It is used only as a policy
	// source; nested tool execution uses a dispatcher built from the restricted
	// registry.
	Dispatcher *runtime.Dispatcher
	// Model is the model name to use.
	Model string
	// Reasoning is the dial configured for Model, applied to every step of the
	// nested loop so a delegated task does not silently run at a different
	// reasoning depth than the task that spawned it.
	Reasoning reasoning.Setting
	// ReasoningFunc reads a session-owned dial at invocation time. It
	// supersedes Reasoning when present, so a runtime effort choice reaches a
	// handler built before the choice was made.
	ReasoningFunc func() reasoning.Setting
	// SystemPrompt is the system prompt for the sub-agent.
	SystemPrompt string
	// MemoryContext is the rendered core-memory context frame
	// (chat.MemoryContextContent), delivered as a user-role message right
	// after the system message rather than composed into SystemPrompt, so
	// memory changes never touch the cached system-prompt prefix. Empty
	// means no memory injection.
	MemoryContext string
	// MaxSteps is the maximum number of LLM turns.
	MaxSteps int
	// WorkLimits bounds cumulative provider and tool work for this invocation.
	WorkLimits runtime.WorkLimits
	// DisableProviderReplay prevents provider-internal replays for this work.
	DisableProviderReplay bool
	// WireStreamTransport opts every nested turn of this handler into the
	// provider's wire-stream transport: stream:true on the wire, the
	// non-stream contract on the return path. The content-idle watchdog in
	// the provider layer then bounds each attempt, so a keepalive trickle
	// cannot hold a nested turn open past a deterministic bound. Set from
	// [subagents] wire_stream at every construction site.
	WireStreamTransport bool
	// ToolTimeout is the per-tool-call timeout.
	ToolTimeout time.Duration
	// ToolRunTimeout is the [tools] tool_run_timeout_seconds knob: the SDK
	// tool-registry's registry-wide run backstop for tools with no declared
	// Capability.Timeout. <= 0 (the default) means no registry-wide cap
	// (mapped to the SDK's TimeoutNone); see agent.Options.ToolRunTimeout.
	ToolRunTimeout time.Duration
	// RequestTimeout is the per-LLM-request timeout for each turn inside the
	// sub-agent. When zero, the agent loop falls back to the parent context
	// deadline, which may be hours for a long-running root session. A hung LLM
	// call would then block the subagent indefinitely. Handlers set this from
	// [subagents] default_request_timeout_seconds; when that knob is unset,
	// they apply DefaultSubagentRequestTimeoutSec (1800s, 30 minutes) - the
	// 12-hour orchestration default no longer feeds individual requests. The
	// derived http.Client wall stays above this budget plus a margin, so the
	// budget is what ends an overlong request.
	RequestTimeout time.Duration
	// SteerWatchdog bounds steer latency when no interrupt signal is wired: the
	// loop's watcher cancels the in-flight LLM call once a steer has been
	// pending for this long (plan 54 §4.5). 0 disables the watchdog.
	SteerWatchdog time.Duration
	// TotalTimeout is the maximum wall-clock time for the entire sub-agent.
	TotalTimeout time.Duration
	// MaxTokens is the max tokens per LLM response.
	MaxTokens int
	// MaxContextTokens is the local prompt budget for every nested request.
	MaxContextTokens int
	// MaxContextTokensFunc reads a session-owned budget at invocation time.
	// It supersedes MaxContextTokens when present.
	MaxContextTokensFunc func() int
	// MaxToolResultChars caps each tool result stored in the nested loop's
	// history, in bytes. 0 means uncapped. Set from the same
	// [tools] max_tool_result_bytes knob as the interactive session loop.
	MaxToolResultChars int
	// BatchResultBudgetBytes bounds what one nested tool batch adds to the
	// sub-agent's history across all its parallel calls. Same operator knob as
	// the interactive loop ([tools] batch_result_budget_bytes); the counter is
	// per batch, so every nested loop gets its own automatically.
	BatchResultBudgetBytes int
	// RefOnlyTools names tools whose results are always spooled as refs by the
	// nested loop. Same operator knob as the interactive loop
	// ([tools] ref_only_tools); empty = off.
	RefOnlyTools []string
	// RemainderSpool stores truncated tool-result bodies for read_output.
	// Shared with the session's registered read_output tool so notices and
	// reads use one grant domain. Nil omits refs from truncation notices.
	RemainderSpool *remainder.Spool
	// OutputSchema is the handler-default schema (skill/agent). Request.OutputSchema
	// overrides when set. Nil means free-text output.
	OutputSchema map[string]any
	// SchemaRetryMax is corrective re-entries after the first invalid reply.
	// <=0 uses default 2.
	SchemaRetryMax int
	// OnEvent is called for sub-agent tool events (optional, for TUI).
	OnEvent func(agent.Event)
	// ContextPreparationManager is deliberately the preparation-only capability.
	// A nested handler never receives a context store or checkpoint publisher.
	ContextPreparationManager contextmgr.PreparationManager
	ContextPreparationInput   contextmgr.PrepareInput
	// OnToolCancelReady, when set, is forwarded as this invocation's nested
	// agent.Options.OnToolCancelReady: the SDK backend calls it once, as
	// soon as the run's per-turn cancel registry exists, with a
	// ToolCanceler the host can retain and invoke later to cancel ONE
	// in-flight tool call within THIS task without aborting the task, any
	// sibling task, or the parent run.
	//
	// ctx is the same context Invoke/run received - it carries
	// runtime.TaskIdentity when this invocation was dispatched by a
	// coordinator-owned subagents.Pool (contextForTask stamps it before
	// Pool.executeOne calls the dispatcher), which is how a host keys its
	// own registry without MultiStepHandler needing to know anything
	// coordinator-specific. A handler that never sets this field (every
	// construction site before this one) is unaffected: the nested loop's
	// OnToolCancelReady stays nil, exactly as before.
	OnToolCancelReady func(ctx context.Context, canceler agent.ToolCanceler)
}

// Invoke creates a restricted agent loop and runs the assigned task.
func (h *MultiStepHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("multi-step subagent %q: %w", req.Name, ctx.Err())
	}

	var taskPrompt string
	if err := json.Unmarshal(req.Input, &taskPrompt); err != nil {
		return nil, fmt.Errorf("multi-step subagent %q: invalid task input: %w", req.Name, err)
	}
	if strings.TrimSpace(taskPrompt) == "" {
		return nil, fmt.Errorf("multi-step subagent %q: empty task prompt", req.Name)
	}

	return h.run(ctx, taskPrompt, req)
}

// originForRequest builds the attribution stamped onto every event a subagent
// loop emits.
//
// TaskID is the correlation key: coordinator calls carry the workflow
// attempt's task id (wft-...) on the context, so bus, ledger, and attempt
// events share one key; other callers fall back to the request id.
//
// SessionID and TurnID serve a different consumer. The subagent publish path
// reaches the event bus through package-level state that has no session
// context of its own, so an event that does not carry them is published with
// an empty SessionID - and internal/hub's receiver drops every event whose
// SessionID does not match its own, which made every subagent invisible to a
// second live surface.
func originForRequest(ctx context.Context, req runtime.Request) agent.EventOrigin {
	taskID := req.ID
	if id, ok := runtime.TaskIdentityFrom(ctx); ok && id.TaskID != "" {
		taskID = id.TaskID
	}
	origin := agent.EventOrigin{
		TaskID:          taskID,
		Agent:           req.Name,
		Depth:           req.Depth + 1,
		TaskDescription: taskDescriptionFromInput(req.Input),
		SessionID:       req.SessionID,
		TurnID:          req.TurnID,
	}
	// The task that caused this one to start, carried on the request itself.
	// A context value cannot do this job: the coordinator roots every task's
	// context in context.Background(), so nothing a caller puts on its own
	// context is reachable from the task it starts.
	origin.ParentTaskID = req.ParentTaskID
	return origin
}

// announceRunStart emits the run-level opening signal, the mirror of the
// deferred Done in run. It fires before any work, so a consumer learns that
// the run exists, and what it was asked to do, without waiting for the run's
// first nested tool call. A nil sink makes it a no-op.
func announceRunStart(stamped func(agent.Event), name, taskDescription string) {
	if stamped == nil {
		return
	}
	stamped(agent.Event{
		Kind:   agent.EventSubagentBegin,
		Name:   name,
		Detail: taskDescription,
	})
}

func (h *MultiStepHandler) run(ctx context.Context, taskPrompt string, req runtime.Request) (out json.RawMessage, err error) {
	scoped, err := h.newScopedLoop()
	if err != nil {
		return nil, err
	}
	defer scoped.dispatcher.Close()
	loop := scoped.loop
	steps, maxTokens, toolTimeout := h.setupAgentLoop()
	loop.Messages = h.seedMessages()

	// Every event this loop emits - including heartbeats - is stamped with
	// the run's identity so the parent UI can attribute it. Without the
	// stamp, parallel subagents are indistinguishable downstream.
	origin := originForRequest(ctx, req)
	stamped := StampEventOrigin(h.OnEvent, origin)

	announceRunStart(stamped, req.Name, origin.TaskDescription)

	// A run that ends must say so, and say HOW it ended. Nested tool events
	// only ever report tool lifecycle, so without this terminal signal the
	// parent's live agent view cannot distinguish "finished" from "thinking
	// between two tools" and keeps every agent of the turn pinned until the
	// whole turn ends. Status carries the terminal classification
	// (terminalStatus of the named return err). Deferred so cancellation, a
	// provider error, and a panic all announce it. A panicked run recovers
	// here, converts the panic into a wrapped error result, and stamps
	// "error" - the same failed-task outcome the pool's own recovery gives a
	// panicking handler, without a re-panic, which the static rules for this
	// package forbid. A panicked subagent's done event must never say
	// "completed".
	if stamped != nil {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("multi-step subagent %q: panic: %v", req.Name, rec)
				out, _ = buildResult("", 0, 0, 0, err)
			}
			stamped(agent.Event{Kind: agent.EventSubagentDone, Name: req.Name, Status: terminalStatus(err)})
		}()
	}

	// Apply total timeout if specified - but only if it's tighter than parent.
	// Never extend beyond parent deadline (that's the orchestrator's call).
	// Derived AFTER ctx carries this run's origin so callCtx inherits it.
	callCtx, cancel := h.timeoutContext(ctx, req)
	defer cancel()

	compiled, appendix, cerr := h.compileOutputSchema(req)
	if cerr != nil {
		return buildResult("", 0, 0, 0, cerr)
	}
	taskPrompt += appendix

	opts := h.loopOptions(scoped, steps, maxTokens, toolTimeout, req, taskPrompt)
	// See OnToolCancelReady's own doc comment: ctx here (not callCtx, derived
	// below) is the one runtime.TaskIdentity was stamped onto, and the one
	// originForRequest already reads it from.
	if h.OnToolCancelReady != nil {
		opts.OnToolCancelReady = func(canceler agent.ToolCanceler) {
			h.OnToolCancelReady(ctx, canceler)
		}
	}
	// Parent→child steers (plan 54): step-boundary drain, soft interrupt of the
	// in-flight LLM call, pending gate, watchdog, and cooldown. The mailbox
	// bundle is optional; without one all steer machinery stays off.
	h.applyMailboxAccess(callCtx, &opts)
	heartbeatCtx, heartbeatStop := context.WithCancel(callCtx)
	var stepCount, toolCallCount atomic.Int64
	taskStart := time.Now()
	// One wrap-up deadline notice at the threshold; rationale in deadline_notice.go.
	applyDeadlineNotice(callCtx, taskStart, nil, &opts)
	defer heartbeatStop()
	go emitHeartbeat(heartbeatCtx, stamped, &stepCount, &toolCallCount)
	opts.OnEvent = h.stepOnEvent(ctx, stamped, &stepCount, &toolCallCount, taskStart)

	reply, structured, runErr := h.runValidatedReply(callCtx, loop, opts, taskPrompt, compiled, steps, &stepCount)
	h.discardPreparation(loop)
	return finishRun(loop, reply, structured, time.Since(taskStart), stepCount.Load(), runErr)
}

// toolStartDedup remembers which tool calls the loop's tool_start stream has
// already reported, because that stream carries TWO EventToolStart events per
// call: "queued" from the PointPreTool hook and "running" from the dispatcher
// shim (internal/agent/sdk_tool_events.go states the pair;
// internal/agent/agentloop_maxconcurrent_test.go pins it - 3 calls, 6 events).
// Both legs carry the same ToolCallID, so anything that counts the raw events
// reports exactly twice the tools that ran - which is what the sidebar's
// "Tools: N" and inspect_agents' progress.tool_calls did.
//
// A start with an empty ToolCallID is never deduped: it cannot be matched to a
// sibling leg, and collapsing those would UNDER-count.
type toolStartDedup struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// first reports whether id is the first tool_start seen for that call, and
// records it. Tool calls run concurrently, so this is the synchronization
// point for the set.
func (d *toolStartDedup) first(id string) bool {
	if id == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.seen[id]; dup {
		return false
	}
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	d.seen[id] = struct{}{}
	return true
}

// stepOnEvent builds the nested loop's OnEvent callback: it counts steps,
// forwards tool_start/tool_end events to a ToolCallSink installed on the
// ORIGINAL request context (reqCtx - not the timeout-derived callCtx, which
// never carries values the coordinator didn't put there) when one is
// present, and always forwards to the origin-stamped live/TUI sink. The
// sink forwarding is additive: it records the same events the stamped
// forwarding already sees, for later persistence, without altering what
// stamped receives or when.
//
// A step or tool-start event also fires an immediate EventSubagentHeartbeat
// alongside the raw forward. emitHeartbeat's 30s ticker exists to prove
// liveness during SILENCE (one long LLM call with nothing observable in
// between); it is not fast enough to be the sidebar's only progress source
// - a run that finishes, or makes many steps, well inside 30s would
// otherwise show a stale or zero Elapsed/Tools/Step reading for most or all
// of its life. Both paths share heartbeatDetail, so the two update sources
// can never format the count differently.
//
// One tool call is counted and recorded ONCE: the loop emits two
// EventToolStart events per call and only the first reaches the counter and
// the sink (see toolStartDedup). The second is still forwarded to stamped -
// the operator wire shape is a pinned contract - but it is not progress.
func (h *MultiStepHandler) stepOnEvent(reqCtx context.Context, stamped func(agent.Event), stepCount, toolCallCount *atomic.Int64, taskStart time.Time) func(agent.Event) {
	sink, hasSink := ToolCallSinkFrom(reqCtx)
	starts := &toolStartDedup{}
	return func(e agent.Event) {
		progressed := false
		if e.Kind == agent.EventStep {
			stepCount.Add(1)
			progressed = true
		}
		if e.Kind == agent.EventToolStart {
			if !starts.first(e.ToolCallID) {
				if stamped != nil {
					stamped(e)
				}
				return
			}
			toolCallCount.Add(1)
			progressed = true
		}
		if hasSink {
			if step, ok := toolCallStepFromEvent(e, time.Now()); ok {
				sink(step)
			}
		}
		if stamped != nil {
			stamped(e)
			if progressed {
				stamped(agent.Event{
					Kind:   agent.EventSubagentHeartbeat,
					Detail: heartbeatDetail(time.Since(taskStart), stepCount.Load(), toolCallCount.Load()),
				})
			}
		}
	}
}

// emitHeartbeat runs in a goroutine, emitting periodic heartbeat events
// so the orchestrator/TUI can see that a subagent is still alive.
// Stops when ctx is canceled.
func emitHeartbeat(ctx context.Context, onEvent func(agent.Event), stepCount, toolCallCount *atomic.Int64) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if onEvent != nil {
				onEvent(agent.Event{
					Kind:   agent.EventSubagentHeartbeat,
					Detail: heartbeatDetail(time.Since(start), stepCount.Load(), toolCallCount.Load()),
				})
			}
		}
	}
}

// heartbeatDetail formats one heartbeat's Detail string. The sidebar panel
// parses this (heartbeatStep/heartbeatToolCalls in
// internal/uiadapter/event_kind.go) to drive its Step and Tool calls
// counters, so the field order and key names are a contract with that
// parser - elapsed is rounded to the second to match the pre-existing
// "elapsed=Xs" shape.
func heartbeatDetail(elapsed time.Duration, steps, toolCalls int64) string {
	return fmt.Sprintf("elapsed=%s steps=%d toolcalls=%d", elapsed.Round(time.Second), steps, toolCalls)
}

// Ensure MultiStepHandler implements runtime.Handler at compile time.
var _ runtime.Handler = (*MultiStepHandler)(nil)
