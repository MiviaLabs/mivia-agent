package uiadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// SubagentThreads implements ports.SubagentThreads by dynamically resolving
// threads registered during runtime subagent executions.
type SubagentThreads struct {
	mu      sync.Mutex
	threads map[string]ports.Conversation
}

// Compile-time check that SubagentThreads satisfies ports.SubagentThreads.
var _ ports.SubagentThreads = (*SubagentThreads)(nil)

// NewSubagentThreads creates a new SubagentThreads registry.
func NewSubagentThreads() *SubagentThreads {
	return &SubagentThreads{
		threads: make(map[string]ports.Conversation),
	}
}

// RegisterThread adds or replaces an active conversation thread for a tool call ID.
func (s *SubagentThreads) RegisterThread(callID string, conv ports.Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads[callID] = conv
}

// Thread retrieves the conversation thread for a given tool call ID.
func (s *SubagentThreads) Thread(callID string) (ports.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.threads[callID]
	return c, ok
}

// HandleEvent receives subagent-originated agent.Events, translates them,
// records their history, and routes them to the matching SubagentTranscriptConversation.
func (s *SubagentThreads) HandleEvent(ev agent.Event, opts TranslateOptions) {
	if ev.Origin.IsZero() {
		return
	}
	keys := []string{ev.Origin.TaskID, ev.ToolCallID, ev.Origin.Agent}
	var hasKey bool
	for _, k := range keys {
		if k != "" {
			hasKey = true
			break
		}
	}
	if !hasKey {
		return
	}

	conv := s.getOrCreate(keys, ev.Origin.Agent)
	translated := TranslateEventWithOptions(ev, opts)
	for _, e := range translated {
		conv.RecordEvent(e)
	}
}

func (s *SubagentThreads) getOrCreate(keys []string, title string) *SubagentTranscriptConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if c, ok := s.threads[k]; ok {
			if stc, ok := c.(*SubagentTranscriptConversation); ok {
				for _, other := range keys {
					if other != "" {
						s.threads[other] = stc
					}
				}
				return stc
			}
		}
	}
	if title == "" {
		for _, k := range keys {
			if k != "" {
				title = k
				break
			}
		}
	}
	stc := NewSubagentTranscriptConversation(title, ports.ModelInfo{Name: title}, nil)
	for _, k := range keys {
		if k != "" {
			s.threads[k] = stc
		}
	}
	return stc
}

// SubagentTranscriptConversation represents a subagent transcript thread.
type SubagentTranscriptConversation struct {
	title     string
	model     ports.ModelInfo
	mu        sync.Mutex
	history   []ports.Message
	listeners []chan uievent.Event
}

// NewSubagentTranscriptConversation creates a new thread conversation.
func NewSubagentTranscriptConversation(title string, model ports.ModelInfo, history []ports.Message) *SubagentTranscriptConversation {
	var histCopy []ports.Message
	if len(history) > 0 {
		histCopy = make([]ports.Message, len(history))
		copy(histCopy, history)
	}
	return &SubagentTranscriptConversation{
		title:   title,
		model:   model,
		history: histCopy,
	}
}

// RecordEvent records one translated uievent into message history and notifies listeners.
func (c *SubagentTranscriptConversation) RecordEvent(e uievent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch e.Kind {
	case uievent.KindTurnStart:
		body, _ := e.Body.(uievent.TurnStartBody)
		c.history = append(c.history, ports.Message{
			Role: "user",
			Text: body.Input,
			At:   e.At,
		})
	case uievent.KindReasoning:
		body, _ := e.Body.(uievent.ReasoningDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Reasoning += body.Text
	case uievent.KindToolStart:
		body, _ := e.Body.(uievent.ToolStartBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		argsBytes, _ := json.Marshal(body.Args)
		c.history[lastIdx].ToolCalls = append(c.history[lastIdx].ToolCalls, ports.ToolCall{
			ID:        body.ToolCallID,
			Name:      body.Name,
			Arguments: string(argsBytes),
		})
	case uievent.KindToolEnd:
		body, _ := e.Body.(uievent.ToolEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		for i := range c.history[lastIdx].ToolCalls {
			if c.history[lastIdx].ToolCalls[i].ID == body.ToolCallID {
				c.history[lastIdx].ToolCalls[i].Output = body.Result
				break
			}
		}
		if body.Diff != nil {
			c.history[lastIdx].Diffs = append(c.history[lastIdx].Diffs, *body.Diff)
		}
	case uievent.KindTextDelta:
		body, _ := e.Body.(uievent.TextDeltaBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		c.history[lastIdx].Text += body.Text
	case uievent.KindTextEnd:
		body, _ := e.Body.(uievent.TextEndBody)
		c.ensureLastAssistantMessage(e.At)
		lastIdx := len(c.history) - 1
		if c.history[lastIdx].Text == "" {
			c.history[lastIdx].Text = body.Text
		}
	}

	for _, ch := range c.listeners {
		select {
		case ch <- e:
		default:
		}
	}
	if e.Kind == uievent.KindTurnEnd {
		for _, ch := range c.listeners {
			close(ch)
		}
		c.listeners = nil
	}
}

func (c *SubagentTranscriptConversation) ensureLastAssistantMessage(at time.Time) {
	if len(c.history) == 0 || c.history[len(c.history)-1].Role != "assistant" {
		c.history = append(c.history, ports.Message{
			Role: "assistant",
			At:   at,
		})
	}
}

// ActiveTurn returns a live event subscription for the active subagent.
func (c *SubagentTranscriptConversation) ActiveTurn() (ports.TurnHandle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	id := fmt.Sprintf("%s-live", c.title)
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, true
}

// Send records user messages in the thread and emits transcript stream events.
func (c *SubagentTranscriptConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.mu.Lock()
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	ch := make(chan uievent.Event, 32)
	c.listeners = append(c.listeners, ch)
	c.mu.Unlock()

	id := fmt.Sprintf("%s-turn-%d", c.title, len(c.history))
	h := &subagentTurnHandle{
		id:     id,
		events: ch,
		cancel: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, l := range c.listeners {
				if l == ch {
					c.listeners = append(c.listeners[:i], c.listeners[i+1:]...)
					close(ch)
					break
				}
			}
		},
	}
	return h, nil
}

// History returns a copy of the thread history.
func (c *SubagentTranscriptConversation) History() []ports.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ports.Message, len(c.history))
	copy(out, c.history)
	return out
}

// Model returns the subagent model information.
func (c *SubagentTranscriptConversation) Model() ports.ModelInfo {
	return c.model
}

// ContextUsage returns token usage for the subagent thread.
func (c *SubagentTranscriptConversation) ContextUsage() ports.Usage {
	return ports.Usage{}
}

// Title returns the title of the subagent thread.
func (c *SubagentTranscriptConversation) Title() string {
	if strings.TrimSpace(c.title) == "" {
		return "Subagent Thread"
	}
	return c.title
}

// ID returns the subagent thread ID.
func (c *SubagentTranscriptConversation) ID() string {
	return c.title
}

type subagentTurnHandle struct {
	id     string
	events chan uievent.Event
	cancel func()
	once   sync.Once
}

func (h *subagentTurnHandle) ID() string                   { return h.id }
func (h *subagentTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *subagentTurnHandle) Cancel() {
	h.once.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
}

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

func populateDispatchTasks(threads *SubagentThreads, tc ports.ToolCall, at time.Time) {
	var args struct {
		Tasks []parsedDispatchTask `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(tc.Arguments), &args)

	var out struct {
		Tasks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Output string `json:"output"`
			Result string `json:"result"`
			Agent  string `json:"agent"`
		} `json:"tasks"`
	}
	_ = json.Unmarshal([]byte(tc.Output), &out)

	outputsByID := make(map[string]string)
	for _, ot := range out.Tasks {
		res := ot.Output
		if res == "" {
			res = ot.Result
		}
		if ot.ID != "" {
			outputsByID[ot.ID] = res
		}
	}

	for i, task := range args.Tasks {
		outputText := outputsByID[task.ID]
		if outputText == "" && len(out.Tasks) == len(args.Tasks) {
			res := out.Tasks[i].Output
			if res == "" {
				res = out.Tasks[i].Result
			}
			outputText = res
		}
		if outputText == "" && len(args.Tasks) == 1 {
			outputText = tc.Output
		}
		registerDispatchedTask(threads, tc.ID, i, task, outputText, at)
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
	conv := NewSubagentTranscriptConversation("dispatch_tasks", ports.ModelInfo{Name: "dispatch_tasks"}, history)
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
