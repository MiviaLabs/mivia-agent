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
	if lower == "dispatch_tasks" {
		populateDispatchTasks(threads, tc, at)
		return
	}
	if lower == "spawn_agent" {
		populateSpawnAgentTasks(threads, tc, at)
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

	conv := NewSubagentTranscriptConversation(agentName, ports.ModelInfo{Name: agentName}, history)
	threads.RegisterThread(tc.ID, conv)
	if agentName != "" {
		threads.RegisterThread(agentName, conv)
	}
}

type parsedDispatchTask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Task   string `json:"task"`
	Agent  string `json:"agent"`
	Skill  string `json:"skill"`
}

// encodedTaskResult is one dispatched task's persisted result, matching both
// dispatchTaskResult (internal/cliorchestrate/dispatch.go) and
// modelTaskResult (internal/cliorchestrate/orchestrate_lifecycle.go) - the
// two producers share this wire shape, keyed "task_id" (not "id"), with
// Output as raw JSON since a subagent's own output may itself be a JSON
// object rather than a plain string.
type encodedTaskResult struct {
	TaskID string          `json:"task_id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Error  string          `json:"error"`
	Agent  string          `json:"agent"`
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

// matchTaskOutputs pairs each dispatched task with its result text, by
// task ID first, falling back to positional matching when IDs are absent
// but the counts agree, and finally to the run-level error envelope (or raw
// payload) when there are zero per-task results parsed at all - e.g. the
// whole run failed before producing any.
func matchTaskOutputs(results []encodedTaskResult, tasks []parsedDispatchTask, rawOutput string) []string {
	byID := make(map[string]string, len(results))
	for _, r := range results {
		text := stringifyTaskOutput(r.Output)
		if text == "" {
			text = r.Error
		}
		if r.TaskID != "" {
			byID[r.TaskID] = text
		}
	}
	out := make([]string, len(tasks))
	for i, task := range tasks {
		text := byID[task.ID]
		if text == "" && len(results) == len(tasks) {
			text = stringifyTaskOutput(results[i].Output)
			if text == "" {
				text = results[i].Error
			}
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

func populateDispatchTasks(threads *SubagentThreads, tc ports.ToolCall, at time.Time) {
	var args struct {
		Tasks []parsedDispatchTask `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(tc.Arguments), &args)

	// dispatchTasksTool.encodeResults marshals a bare JSON array, not
	// {"tasks":[...]} - see internal/cliorchestrate/dispatch.go:268-278.
	var results []encodedTaskResult
	_ = json.Unmarshal([]byte(tc.Output), &results)

	outputs := matchTaskOutputs(results, args.Tasks, tc.Output)
	for i, task := range args.Tasks {
		registerDispatchedTask(threads, tc.ID, i, task, outputs[i], at)
	}

	if len(args.Tasks) == 0 {
		registerFallbackDispatchedThread(threads, tc, at)
	}
}

// populateSpawnAgentTasks reconstructs spawn_agent's dispatched tasks.
// spawn_agent's Arguments share dispatch_tasks' per-task field names (id,
// prompt, agent, skill - parsedDispatchTask covers both), but its Output is
// wrapped in {"task_results":[...]} rather than a bare array
// (spawnResultPayload, internal/cliorchestrate/orchestrate.go:174-235).
func populateSpawnAgentTasks(threads *SubagentThreads, tc ports.ToolCall, at time.Time) {
	var args struct {
		Tasks []parsedDispatchTask `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(tc.Arguments), &args)

	var out struct {
		TaskResults []encodedTaskResult `json:"task_results"`
	}
	_ = json.Unmarshal([]byte(tc.Output), &out)

	outputs := matchTaskOutputs(out.TaskResults, args.Tasks, tc.Output)
	for i, task := range args.Tasks {
		registerDispatchedTask(threads, tc.ID, i, task, outputs[i], at)
	}

	if len(args.Tasks) == 0 {
		registerFallbackDispatchedThread(threads, tc, at)
	}
}

func registerDispatchedTask(threads *SubagentThreads, callID string, idx int, task parsedDispatchTask, outputText string, at time.Time) {
	taskID := task.ID
	if taskID == "" {
		taskID = fmt.Sprintf("%s-%d", callID, idx+1)
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
	if outputText != "" {
		history = append(history, ports.Message{Role: "assistant", Text: outputText, At: at})
	}

	conv := NewSubagentTranscriptConversation(agentName, ports.ModelInfo{Name: agentName}, history)
	threads.RegisterThread(taskID, conv)
	threads.RegisterThread(callID, conv)
	if agentName != "" {
		threads.RegisterThread(agentName, conv)
	}
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
	conv := NewSubagentTranscriptConversation(name, ports.ModelInfo{Name: name}, history)
	threads.RegisterThread(tc.ID, conv)
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
