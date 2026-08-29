package uiadapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// PopulateFromToolCalls scans conversation messages for subagent tool invocations
// (such as dispatch_tasks, delegate, spawn_agent, invoke_subagent, and agent_* tools)
// and seeds SubagentTranscriptConversation instances into threads so resumed sessions
// show full subagent history when opening their threads in the TUI.
func PopulateFromToolCalls(threads *SubagentThreads, msgs []ports.Message) {
	if threads == nil {
		return
	}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if !isSubagentToolName(tc.Name) {
				continue
			}
			populateToolCall(threads, tc, m.At)
		}
	}
}

func isSubagentToolName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "agent_") ||
		strings.HasPrefix(lower, "subagent") ||
		strings.HasPrefix(lower, "delegate") ||
		strings.HasPrefix(lower, "invoke_") ||
		strings.HasPrefix(lower, "workflow_") ||
		strings.HasPrefix(lower, "dispatch_") ||
		strings.HasPrefix(lower, "spawn_") ||
		strings.HasPrefix(lower, "send_to_task") ||
		strings.Contains(lower, "orchestrat") ||
		strings.Contains(lower, "planner") ||
		strings.Contains(lower, "builder") ||
		strings.Contains(lower, "reviewer") ||
		strings.Contains(lower, "research")
}

func populateToolCall(threads *SubagentThreads, tc ports.ToolCall, at time.Time) {
	if threads == nil {
		return
	}

	lower := strings.ToLower(tc.Name)
	if lower == "dispatch_tasks" || lower == "spawn_agent" {
		// namespaceTasks is dispatch_tasks-only: spawn_agent has no live
		// backend implementation left (internal/cliorchestrate/dispatch.go's
		// own doc history: "dispatch_tasks absorbed spawn_agent's
		// idempotency_key dedup") - its name survives only in OLD sessions'
		// persisted tool calls, which were never minted with a namespaced
		// real id, so there is no live counterpart to stay consistent with.
		populateDispatchTasks(threads, tc, at, lower == "dispatch_tasks")
		return
	}

	prompt, agentName := extractPromptAndAgent(tc.Arguments)
	if agentName == "" {
		agentName = tc.Name
	}
	output := extractToolOutput(tc.Output)

	var history []ports.Message
	if prompt != "" {
		history = append(history, ports.Message{
			Role: "user",
			Text: prompt,
			At:   at,
		})
	}
	if output != "" {
		history = append(history, ports.Message{
			Role: "assistant",
			Text: output,
			At:   at,
		})
	}

	// tc.ID is always unique and always present; agent name is not (see
	// registerDispatchedTask's identical fix) - a second unrelated
	// single-call dispatch to the same named agent must not collide with
	// this one's reconstructed thread.
	conv := newReconstructedConversation(agentName, ports.ModelInfo{Name: agentName}, history)
	threads.registerReconstructed(tc.ID, conv)
}

type parsedDispatchTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Task   string `json:"task"`
	Agent  string `json:"agent"`
	Skill  string `json:"skill"`
}

// encodedTaskResult is one dispatched task's persisted result, matching both
// dispatchTaskResult (internal/cliorchestrate/dispatch_encode.go) and
// modelTaskResult (internal/cliorchestrate/orchestrate_lifecycle.go) - the
// two producers share this wire shape, keyed "task_id" (not "id"), with
// Output as raw JSON since a subagent's own output may itself be a JSON
// object rather than a plain string.
//
// Synopsis and OutputRef mirror dispatchTaskResult's output-by-reference
// fields: once a task's real output exceeds the inline threshold
// (setOutputFields), Output is omitted entirely and only Synopsis/OutputRef
// are set. Without these, a resumed session's reconstruction had no output
// text to fall back to for any task whose result went by-reference -
// exactly the common case for a substantial subagent answer - and rendered
// the dispatched prompt with nothing after it.
type encodedTaskResult struct {
	TaskID    string          `json:"task_id"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
	Synopsis  string          `json:"synopsis"`
	OutputRef string          `json:"output_ref"`
	Error     string          `json:"error"`
	Agent     string          `json:"agent"`
	// ToolCallsRef is the CURRENT wire shape: the recorded tool-call trace
	// travels by reference only (dispatchTaskResult.ToolCallsRef).
	ToolCallsRef string `json:"tool_calls_ref"`
	// ToolCalls is the LEGACY inline shape. No producer emits it anymore;
	// it survives only in OLD sessions' persisted tool calls, which this
	// decode must keep reconstructing.
	ToolCalls []toolCallSummary `json:"tool_calls"`
}

// toolCallSummary is the LEGACY inline row shape old persisted sessions
// carry; cliorchestrate no longer produces it (the trace now travels behind
// tool_calls_ref), so this package owns the decode alone, pinned by the
// frozen testdata/tool_calls_contract.json fixture. Rows arrived already
// merged one-row-per-call - Incomplete is true only for a genuinely
// unfinished call, never an envelope-cap artifact, so this package can
// trust it directly with no further pairing logic.
type toolCallSummary struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Input      string `json:"input,omitempty"`
	Output     string `json:"output,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// stringifyTaskOutput renders one task's raw Output field as display text.
// The raw bytes may be a JSON string (the common case), a JSON object with
// its own nested "output"/"result"/"response"/"content"/"text" key (a
// subagent's own structured reply, embedded as-is per
// cliorchestrate.ModelVisibleOutput), or - as a last resort, when neither
// shape matches - the raw JSON text itself, so a genuinely unrecognized
// payload is still visible rather than silently dropped.
func stringifyTaskOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, key := range []string{"output", "result", "response", "content", "text"} {
			if val, ok := m[key]; ok {
				if sv, ok := val.(string); ok && sv != "" {
					return sv
				}
			}
		}
	}
	return string(raw)
}

// rawErrorEnvelopeText renders a run-level failure envelope
// ({"error":...,"status":...}, returned when a whole dispatch_tasks/
// spawn_agent run fails before any per-task result exists) as readable
// text, falling back to the raw payload only when it does not match that
// shape at all.
func rawErrorEnvelopeText(raw string) string {
	var env struct {
		Error  string `json:"error"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(raw), &env) == nil && env.Error != "" {
		if env.Status != "" {
			return env.Error + " (status: " + env.Status + ")"
		}
		return env.Error
	}
	return raw
}

// resultText renders one task's display text: the real inline Output when
// present, else the synopsis dispatch_tasks reports for an above-threshold
// result that went by-reference (setOutputFields in
// internal/cliorchestrate/dispatch_encode.go), else the task's error. A
// result whose only content is a recorded trace reference still gets a
// visible notice - never a silent empty thread - but the raw ref stays out
// of display text (a reconstructed-thread reader has no ledger_read).
func resultText(r encodedTaskResult) string {
	if text := stringifyTaskOutput(r.Output); text != "" {
		return text
	}
	if r.Synopsis != "" {
		return r.Synopsis
	}
	if r.Error != "" {
		return r.Error
	}
	if r.ToolCallsRef != "" {
		return "(tool calls recorded)"
	}
	return ""
}

// matchTaskOutputs pairs each dispatched task with its result text, by
// task ID first, falling back to positional matching when the ID is ABSENT
// from the results but the counts agree, and finally to the run-level error
// envelope (or raw payload) when there are zero per-task results parsed at
// all - e.g. the whole run failed before producing any.
//
// The ID-present rule (not empty-text) deliberately matches
// matchTaskToolCalls: an ID match whose text is empty means the task
// genuinely produced no text, not that a positional row is a better source.
// The original empty-text fallback (2b5426a9) was part of the generic
// positional fallback, not a fix for a real empty-text case, and it let a
// task render ANOTHER task's output text above its own tool calls whenever
// results arrived out of order.
func matchTaskOutputs(results []encodedTaskResult, tasks []parsedDispatchTask, rawOutput string) []string {
	byID := make(map[string]string, len(results))
	for _, r := range results {
		text := resultText(r)
		if r.TaskID != "" {
			byID[r.TaskID] = text
		}
	}
	out := make([]string, len(tasks))
	for i, task := range tasks {
		text, ok := byID[task.ID]
		if !ok && len(results) == len(tasks) {
			text = resultText(results[i])
		}
		if text == "" && len(results) == 0 {
			// A whole-run failure (spawn/validation/join error) returns the
			// bare {"error":...,"status":...} envelope with zero per-task
			// results regardless of how many tasks were dispatched - every
			// dispatched task gets the same readable envelope text rather
			// than silent empty output.
			text = rawErrorEnvelopeText(rawOutput)
		}
		out[i] = text
	}
	return out
}

// matchTaskToolCalls pairs each dispatched task with its already-merged
// tool-call summaries (see toolCallSummary), by task ID first and falling
// back to positional matching when IDs are absent but the counts agree -
// mirroring matchTaskOutputs' own pairing rules exactly so a task's output
// text and its tool calls always come from the same result row.
func matchTaskToolCalls(results []encodedTaskResult, tasks []parsedDispatchTask) [][]toolCallSummary {
	byID := make(map[string][]toolCallSummary, len(results))
	for _, r := range results {
		if r.TaskID != "" {
			byID[r.TaskID] = r.ToolCalls
		}
	}
	out := make([][]toolCallSummary, len(tasks))
	for i, task := range tasks {
		calls, ok := byID[task.ID]
		if !ok && len(results) == len(tasks) {
			calls = results[i].ToolCalls
		}
		out[i] = calls
	}
	return out
}

func populateDispatchTasks(threads *SubagentThreads, tc ports.ToolCall, at time.Time, namespaceIDs bool) {
	var args struct {
		Tasks []parsedDispatchTask `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(tc.Arguments), &args)

	// dispatch_tasks may emit either a bare JSON array (wait="run", default)
	// or a wrapped JSON envelope {"run_id":..., "status":..., "task_results":[...]}
	// (wait="none" or wait="task", and legacy spawn_agent output). Both the
	// persisted args and the persisted result carry each task's RAW
	// model-supplied id, not a namespaced one - dispatch_tasks strips its
	// own internal namespace prefix (internal/cliorchestrate/dispatch.go's
	// stripNamespace) from every model-visible output before returning, so
	// what got PERSISTED, and therefore what matchTaskOutputs/
	// matchTaskToolCalls must match by, is always the raw id.
	var results []encodedTaskResult
	if err := json.Unmarshal([]byte(tc.Output), &results); err != nil || len(results) == 0 {
		var out struct {
			TaskResults []encodedTaskResult `json:"task_results"`
		}
		if errWrap := json.Unmarshal([]byte(tc.Output), &out); errWrap == nil && len(out.TaskResults) > 0 {
			results = out.TaskResults
		}
	}

	outputs := matchTaskOutputs(results, args.Tasks, tc.Output)
	toolCalls := matchTaskToolCalls(results, args.Tasks)
	for i, task := range args.Tasks {
		// The THREAD KEY - unlike the byID match above - must be
		// namespaced: it has to land on the same id a live observer would
		// have used for this task (dispatchTaskIDs in
		// internal/ui/screen/conversation/events.go, and
		// internal/coordinator/task_context.go's contextForTask, both key
		// live/internal identity off tc.ID+":"+raw id), so a resumed
		// session's sidebar row (built by thread.go's LoadHistory, which
		// namespaces the same way) resolves to this reconstruction.
		keyed := task
		if namespaceIDs && keyed.ID != "" {
			keyed.ID = namespacedTaskID(tc.ID, keyed.ID)
		}
		registerDispatchedTask(threads, tc.ID, i, keyed, outputs[i], toolCalls[i], at)
	}

	if len(args.Tasks) == 0 {
		registerFallbackDispatchedThread(threads, tc, at)
	}
}

func registerDispatchedTask(threads *SubagentThreads, callID string, idx int, task parsedDispatchTask, outputText string, toolCalls []toolCallSummary, at time.Time) {
	taskID := task.ID
	if taskID == "" {
		// Must match dispatchTaskIDs' fallback in
		// internal/ui/screen/conversation/events.go: never embed the raw
		// provider tool_call_id (callID) in a visible row id.
		taskID = fmt.Sprintf("task-%d", idx+1)
	}
	agentName := task.Agent
	if agentName == "" {
		agentName = task.Skill
	}
	if agentName == "" {
		agentName = "subagent"
	}
	prompt := task.Prompt
	if prompt == "" {
		prompt = task.Task
	}

	var history []ports.Message
	if prompt != "" {
		history = append(history, ports.Message{Role: "user", Text: prompt, At: at})
	}
	if outputText != "" || len(toolCalls) > 0 {
		msg := ports.Message{Role: "assistant", Text: outputText, At: at}
		if len(toolCalls) > 0 {
			msg.ToolCalls = make([]ports.ToolCall, len(toolCalls))
			for i, s := range toolCalls {
				msg.ToolCalls[i] = ports.ToolCall{
					ID:        s.ToolCallID,
					Name:      s.Name,
					Arguments: s.Input,
					Output:    s.Output,
				}
			}
		}
		history = append(history, msg)
	}

	// Register non-destructively under task id and call id: either may
	// already point at a live streaming conversation, which must survive
	// any History() replay. Agent name is deliberately NOT a registration
	// key here: taskID always exists in this function (model-supplied or
	// the "task-N" fallback above), and agent name is not unique - two
	// tasks in the same batch (or across batches on resume) commonly
	// share one agent (e.g. "general-purpose"), and keying on it would
	// fold their reconstructed histories into a single shared lookup
	// alias (see HandleEvent's identical fix and
	// TestSubagentThreads_SameAgentDifferentTasksDoNotShareAThread for
	// the live-path version of this bug).
	conv := newReconstructedConversation(agentName, ports.ModelInfo{Name: agentName}, history)
	threads.registerReconstructed(taskID, conv)
	threads.registerReconstructed(callID, conv)
}

func registerFallbackDispatchedThread(threads *SubagentThreads, tc ports.ToolCall, at time.Time) {
	var history []ports.Message
	if tc.Arguments != "" {
		history = append(history, ports.Message{Role: "user", Text: tc.Arguments, At: at})
	}
	if tc.Output != "" {
		history = append(history, ports.Message{Role: "assistant", Text: tc.Output, At: at})
	}
	name := tc.Name
	if name == "" {
		name = "dispatch_tasks"
	}
	conv := newReconstructedConversation(name, ports.ModelInfo{Name: name}, history)
	threads.registerReconstructed(tc.ID, conv)
}

func extractPromptAndAgent(argsJSON string) (prompt, agentName string) {
	if argsJSON == "" {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return argsJSON, ""
	}
	for _, key := range []string{"prompt", "task", "description", "input", "query", "instruction"} {
		if val, ok := m[key]; ok {
			if s, ok := val.(string); ok && s != "" {
				prompt = s
				break
			}
		}
	}
	if prompt == "" {
		if subs, ok := m["Subagents"].([]any); ok && len(subs) > 0 {
			if subMap, ok := subs[0].(map[string]any); ok {
				if p, ok := subMap["Prompt"].(string); ok {
					prompt = p
				}
				if a, ok := subMap["Role"].(string); ok {
					agentName = a
				} else if a, ok := subMap["TypeName"].(string); ok {
					agentName = a
				}
			}
		}
	}
	if agentName == "" {
		for _, key := range []string{"agent", "subagent", "role", "type", "TypeName", "Role", "skill"} {
			if val, ok := m[key]; ok {
				if s, ok := val.(string); ok && s != "" {
					agentName = s
					break
				}
			}
		}
	}
	return prompt, agentName
}

func extractToolOutput(outputJSON string) string {
	if outputJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(outputJSON), &m); err == nil {
		for _, key := range []string{"output", "result", "response", "content"} {
			if val, ok := m[key]; ok {
				if s, ok := val.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return outputJSON
}

// namespacedTaskID mirrors internal/cliorchestrate's function of the same
// name. Duplicated, not imported: internal/uiadapter is the sole
// integration bridge and deliberately does not import the cli-family
// orchestration package (INV-TUI-29), so the two copies are kept in sync
// by contract, not by the compiler.
func namespacedTaskID(namespace, rawID string) string {
	if namespace == "" || rawID == "" {
		return rawID
	}
	return namespace + ":" + rawID
}
