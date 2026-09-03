package events

import (
	"fmt"
	"strings"
	"time"
)

// Kind is the type for event kinds.
type Kind string

const (
	// Agent loop events (mirror agent.EventKind values).
	KindAssistant Kind = "assistant"
	KindToolStart Kind = "tool_start"
	KindToolEnd   Kind = "tool_end"
	KindStep      Kind = "step"
	// KindHeartbeat mirrors agent.EventHeartbeat: a wall-clock progress
	// tick during model thinking, tool batches, and batch shaping. The
	// root loop publishes the bare string "heartbeat". Use this constant
	// to subscribe to it.
	KindHeartbeat         Kind = "heartbeat"
	KindPrune             Kind = "prune"
	KindToolParallel      Kind = "tool_parallel"
	KindSubagentStart     Kind = "subagent_start"
	KindSubagentEnd       Kind = "subagent_end"
	KindSubagentHeartbeat Kind = "subagent_heartbeat"
	// KindSubagentBegin mirrors agent.EventSubagentBegin: the run-level
	// opening signal for one subagent, not the start of a nested tool call.
	KindSubagentBegin Kind = "subagent_begin"
	// KindSubagentDone mirrors agent.EventSubagentDone: the run-level
	// terminal signal for one subagent, not the end of a nested tool call.
	KindSubagentDone Kind = "subagent_done"
	// KindAssistantReset mirrors agent.EventAssistantReset: discard the
	// assistant text accumulated for this turn so far and start again,
	// because the turn is being re-driven from the beginning.
	KindAssistantReset Kind = "assistant_reset"
	KindThinking       Kind = "thinking"
	// KindHook mirrors agent.EventHook: one lifecycle hook ran on the
	// operator's machine. The structured facts - which script, for which
	// tool, and whether it BLOCKED the call - travel in HookEvent, because
	// the bus's generic string conversion carries none of them.
	KindHook       Kind = "hook"
	KindCompaction Kind = "compaction"
	// KindCacheUsage reports provider-supplied prompt-cache accounting for
	// one completion turn. See CacheUsageEvent.
	KindCacheUsage Kind = "cache_usage"
	// KindTokenUsage reports provider-supplied input/output token counts
	// for one completion turn. See TokenUsageEvent.
	KindTokenUsage Kind = "token_usage"
	// KindPrefixReset reports that the session's byte-prefix stability
	// identity changed at a binding switch or agent-surface publication, so a
	// provider-implicit prompt-cache prefix is no longer reusable for the next
	// request. See PrefixResetEvent.
	KindPrefixReset Kind = "prefix_reset"

	// Session/turn lifecycle events.
	KindSessionStart Kind = "session_start"
	KindSessionEnd   Kind = "session_end"
	KindTurnStart    Kind = "turn_start"
	KindTurnEnd      Kind = "turn_end"

	// Workflow and invocation observability events. Run, step, and task
	// identifiers ride in Event.Metadata; no Event fields are added.
	// KindWorkflowRunStarted reports the start of one workflow run.
	KindWorkflowRunStarted Kind = "workflow_run_started"
	// KindWorkflowStepStarted reports the start of one workflow step.
	KindWorkflowStepStarted Kind = "workflow_step_started"
	// KindWorkflowStepHeartbeat is the progress tick of a running step.
	KindWorkflowStepHeartbeat Kind = "workflow_step_heartbeat"
	// KindWorkflowStepCompleted reports the completion of one workflow step.
	KindWorkflowStepCompleted Kind = "workflow_step_completed"
	// KindWorkflowGateResult reports the start of one workflow gate: the gate
	// begin is published at gate_started time; the gate's outcome is published
	// as step_completed when the attempt reaches its terminal status.
	KindWorkflowGateResult Kind = "workflow_gate_result"
	// KindWorkflowApprovalRequested reports a workflow approval request.
	KindWorkflowApprovalRequested Kind = "workflow_approval_requested"
	// KindWorkflowRunFinished reports the end of one workflow run.
	KindWorkflowRunFinished Kind = "workflow_run_finished"
	// KindWorkflowDeliveryStage reports one delivery stage of a workflow.
	KindWorkflowDeliveryStage Kind = "workflow_delivery_stage"
	// KindInvocationStarted reports the start of one invocation.
	KindInvocationStarted Kind = "invocation_started"
	// KindInvocationCompleted reports the completion of one invocation.
	KindInvocationCompleted Kind = "invocation_completed"
	// KindInvocationRetrying reports one retry of an invocation.
	KindInvocationRetrying Kind = "invocation_retrying"

	// UI/system events.
	KindUIResize     Kind = "ui_resize"
	KindUserInput    Kind = "user_input"
	KindUIReady      Kind = "ui_ready"
	KindConfigChange Kind = "config_change"

	// Error events.
	KindError Kind = "error"
)

// Event is the universal event type for the event bus.
type Event struct {
	Kind       Kind
	Timestamp  time.Time
	SessionID  string
	TurnID     string
	ToolCallID string
	Name       string
	Detail     string
	Content    string
	Input      string
	Output     string
	// InputBody and OutputBody are the redacted tool arguments and result
	// WITHOUT the operator preview cap that bounds Input and Output. They
	// exist for a consumer that records its own truncation with a marker
	// (chatsync), so it can report the true size and ship what its budget
	// allows rather than a 512-byte preview cut in silence. Empty on an
	// emitter that predates them; such a consumer falls back to the
	// preview. They are never relayed across processes (hub/wire.go).
	InputBody  string
	OutputBody string
	Metadata   map[string]string
	Err        error

	// Agent attribution: which subagent produced this event (empty for the
	// session's root loop). Flat fields keep this package free of an
	// agent-package dependency.
	AgentTask  string // runtime request/task id - the attribution key
	AgentName  string // dispatched subagent/skill name
	AgentDepth int    // nesting depth (root loop = 0)
	// AgentParent is the AgentTask of the subagent that dispatched this
	// event's producer, empty when the root loop did. Depth alone cannot
	// rebuild the tree: two runs at the same depth under different parents
	// are indistinguishable by depth.
	AgentParent string
	// Identity is the typed, allowlisted runtime identity. It never carries
	// prompts, paths, digests, tools, content, errors, or arbitrary metadata.
	Identity *Identity
	// PrefixReset is present only for the typed prefix-stability reset event
	// (KindPrefixReset). It is not copied into generic content/input/output
	// envelopes and carries no prompt or digest content (INV-68-7).
	PrefixReset *PrefixResetEvent
	// Compaction is present only for the typed context-compaction progress
	// event (KindCompaction). It carries the content-free payload
	// (events.CompactionEvent) so bus consumers - the cross-process hub, a
	// --json sidecar - get the real before/after numbers instead of parsing
	// Detail. Nil on every other kind.
	Compaction *CompactionEvent
	// Hook is present only for KindHook. It carries the structured facts a
	// generic content/input/output envelope cannot: WHICH script ran, for
	// which tool, and above all whether it BLOCKED the call.
	//
	// Those three lived on agent.Event and stopped at the bus boundary, which
	// converts only the generic string fields. A consumer past that boundary -
	// the chat-sync projector, the cross-process relay - therefore could not
	// tell a hook that merely reported from one that refused a tool call, and
	// a refusal is the whole reason the row exists.
	Hook *HookEvent
}

// Identity separates definition, disposable execution instance, and model
// binding generation for operator-facing lifecycle events.
type Identity struct {
	DefinitionName   string
	DefinitionSource string
	InstanceID       string
	ModelGeneration  uint64
}

// NewIdentity validates and copies the public identity contract.
func NewIdentity(name, source, instanceID string, generation uint64) (Identity, error) {
	name = strings.TrimSpace(name)
	source = strings.TrimSpace(source)
	instanceID = strings.TrimSpace(instanceID)
	if name == "" || len(name) > 80 || instanceID == "" || len(instanceID) > 128 || generation == 0 {
		return Identity{}, fmt.Errorf("invalid event identity")
	}
	switch source {
	// "compiled" is retained for replay of ledger/session records written
	// before built-in agents shipped; new compiled content uses "builtin".
	case "user", "workspace", "compiled", "builtin":
	default:
		return Identity{}, fmt.Errorf("invalid event identity")
	}
	for _, value := range []string{name, source, instanceID} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return Identity{}, fmt.Errorf("invalid event identity")
			}
		}
	}
	return Identity{DefinitionName: name, DefinitionSource: source, InstanceID: instanceID, ModelGeneration: generation}, nil
}

// WithAgentAttribution returns a copy of e attributed to a producing agent.
func (e Event) WithAgentAttribution(taskID, name string, depth int) Event {
	e.AgentTask, e.AgentName, e.AgentDepth = taskID, name, depth
	return e
}

// WithAgentParent returns a copy of e whose producer is recorded as a child of
// parentTaskID. Separate from WithAgentAttribution so the many existing call
// sites that have no parent to report keep their present signature.
func (e Event) WithAgentParent(parentTaskID string) Event {
	e.AgentParent = parentTaskID
	return e
}

// NewEvent creates an Event with the given Kind and the current timestamp.
func NewEvent(kind Kind) Event {
	return Event{Kind: kind, Timestamp: time.Now()}
}
