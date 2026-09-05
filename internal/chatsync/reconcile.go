package chatsync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type danglingTool struct {
	toolCallID string
	name       string
	turn       string
}

type danglingSubagent struct {
	task  string
	name  string
	turn  string
	agent *AgentOrigin
}

type danglingSubagentTool struct {
	toolCallID string
	name       string
	turn       string
	agent      *AgentOrigin
}

type danglingCollector struct {
	openTurn          string
	openTools         map[string]danglingTool
	openSubagents     map[string]danglingSubagent
	openSubagentTools map[string]danglingSubagentTool
}

func newDanglingCollector() *danglingCollector {
	return &danglingCollector{
		openTools:         make(map[string]danglingTool),
		openSubagents:     make(map[string]danglingSubagent),
		openSubagentTools: make(map[string]danglingSubagentTool),
	}
}

func (c *danglingCollector) processTurn(se StoredEvent) {
	switch se.Type {
	case TypeTurnStarted:
		var env struct {
			Turn string `json:"turn"`
		}
		_ = json.Unmarshal(se.Payload, &env)
		c.openTurn = env.Turn
		clear(c.openTools)
		clear(c.openSubagents)
		clear(c.openSubagentTools)
	case TypeTurnEnded, TypeTurnFailed:
		c.openTurn = ""
		clear(c.openTools)
		clear(c.openSubagents)
		clear(c.openSubagentTools)
	}
}

func (c *danglingCollector) processTools(se StoredEvent) {
	switch se.Type {
	case TypeToolStarted:
		var payload struct {
			Envelope
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil && payload.ToolCallID != "" {
			c.openTools[payload.ToolCallID] = danglingTool{
				toolCallID: payload.ToolCallID,
				name:       payload.Name,
				turn:       payload.Turn,
			}
		}
	case TypeToolEnded:
		var payload struct {
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil && payload.ToolCallID != "" {
			delete(c.openTools, payload.ToolCallID)
		}
	}
}

func (c *danglingCollector) processSubagents(se StoredEvent) {
	switch se.Type {
	case TypeSubagentStarted:
		var payload struct {
			Envelope
			Name string `json:"name"`
			Task string `json:"task"`
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil {
			task := payload.Task
			if payload.Agent != nil && payload.Agent.Task != "" {
				task = payload.Agent.Task
			}
			if task != "" {
				c.openSubagents[task] = danglingSubagent{
					task:  task,
					name:  payload.Name,
					turn:  payload.Turn,
					agent: payload.Agent,
				}
			}
		}
	case TypeSubagentEnded:
		var payload struct {
			Envelope
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil && payload.Agent != nil && payload.Agent.Task != "" {
			delete(c.openSubagents, payload.Agent.Task)
		}
	case TypeSubagentToolStarted:
		var payload struct {
			Envelope
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil && payload.ToolCallID != "" {
			c.openSubagentTools[payload.ToolCallID] = danglingSubagentTool{
				toolCallID: payload.ToolCallID,
				name:       payload.Name,
				turn:       payload.Turn,
				agent:      payload.Agent,
			}
		}
	case TypeSubagentToolEnded:
		var payload struct {
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(se.Payload, &payload); err == nil && payload.ToolCallID != "" {
			delete(c.openSubagentTools, payload.ToolCallID)
		}
	}
}

func (c *danglingCollector) processEvent(se StoredEvent) {
	c.processTurn(se)
	c.processTools(se)
	c.processSubagents(se)
}

// scanDanglingEvents reads events from events.jsonl and returns open items that
// never received a terminal event.
func scanDanglingEvents(dir string) (string, []danglingTool, []danglingSubagent, []danglingSubagentTool, error) {
	eventsPath := filepath.Join(dir, eventsFileName)
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil, nil, nil
		}
		return "", nil, nil, nil, err
	}
	defer f.Close()

	c := newDanglingCollector()
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var se StoredEvent
		if err := json.Unmarshal(line, &se); err != nil {
			return "", nil, nil, nil, fmt.Errorf("unmarshal stored event: %w", err)
		}
		c.processEvent(se)
	}

	if err := scanner.Err(); err != nil {
		return "", nil, nil, nil, err
	}

	tools := make([]danglingTool, 0, len(c.openTools))
	for _, t := range c.openTools {
		tools = append(tools, t)
	}
	subs := make([]danglingSubagent, 0, len(c.openSubagents))
	for _, s := range c.openSubagents {
		subs = append(subs, s)
	}
	subTools := make([]danglingSubagentTool, 0, len(c.openSubagentTools))
	for _, st := range c.openSubagentTools {
		subTools = append(subTools, st)
	}
	return c.openTurn, tools, subs, subTools, nil
}

func buildSubagentClosingEvents(openTurn string, subs []danglingSubagent, subTools []danglingSubagentTool, writerID string, now time.Time) []WireEvent {
	var closing []WireEvent
	for _, st := range subTools {
		turn := st.turn
		if turn == "" {
			turn = openTurn
		}
		closing = append(closing, WireEvent{
			Type: TypeSubagentToolEnded,
			Payload: SubagentToolEndedPayload{
				Envelope: Envelope{
					V:        1,
					At:       now,
					Turn:     turn,
					Agent:    st.agent,
					WriterID: writerID,
				},
				ToolCallID: st.toolCallID,
				Name:       st.name,
				Status:     "interrupted",
				Detail:     "interrupted by CLI restart",
			},
		})
	}
	for _, sub := range subs {
		turn := sub.turn
		if turn == "" {
			turn = openTurn
		}
		closing = append(closing, WireEvent{
			Type: TypeSubagentEnded,
			Payload: SubagentEndedPayload{
				Envelope: Envelope{
					V:        1,
					At:       now,
					Turn:     turn,
					Agent:    sub.agent,
					WriterID: writerID,
				},
				Name:   sub.name,
				Status: "interrupted",
			},
		})
	}
	return closing
}

func buildToolClosingEvents(openTurn string, tools []danglingTool, writerID string, now time.Time) []WireEvent {
	var closing []WireEvent
	for _, tool := range tools {
		turn := tool.turn
		if turn == "" {
			turn = openTurn
		}
		closing = append(closing, WireEvent{
			Type: TypeToolEnded,
			Payload: ToolEndedPayload{
				Envelope: Envelope{
					V:        1,
					At:       now,
					Turn:     turn,
					WriterID: writerID,
				},
				ToolCallID: tool.toolCallID,
				Name:       tool.name,
				Status:     "interrupted",
				Detail:     "interrupted by CLI restart",
			},
		})
	}
	return closing
}

func buildClosingEvents(openTurn string, tools []danglingTool, subs []danglingSubagent, subTools []danglingSubagentTool, writerID string, now time.Time) []WireEvent {
	closing := buildSubagentClosingEvents(openTurn, subs, subTools, writerID, now)
	closing = append(closing, buildToolClosingEvents(openTurn, tools, writerID, now)...)
	if openTurn != "" {
		closing = append(closing, WireEvent{
			Type: TypeTurnFailed,
			Payload: TurnFailedPayload{
				Envelope: Envelope{
					V:        1,
					At:       now,
					Turn:     openTurn,
					WriterID: writerID,
				},
				Message: "turn interrupted by CLI restart",
			},
		})
	}
	return closing
}

// reconcileDangling synthesizes closing events for any unclosed turns, tools,
// or subagents left by an earlier terminated process.
func (s *SyncSession) reconcileDangling(ctx context.Context) error {
	openTurn, tools, subs, subTools, err := scanDanglingEvents(s.outbox.dir)
	if err != nil {
		return fmt.Errorf("scan dangling events: %w", err)
	}
	if len(tools) == 0 && len(subs) == 0 && len(subTools) == 0 {
		return nil
	}

	writerID := s.opts.ProjectorOptions.WriterID
	closingEvents := buildClosingEvents(openTurn, tools, subs, subTools, writerID, time.Now())
	if len(closingEvents) == 0 {
		return nil
	}

	s.mu.Lock()
	currentSeq := s.projector.LastSeq()
	for i := range closingEvents {
		currentSeq++
		closingEvents[i].Seq = currentSeq
	}
	appendErr := s.appender.Append(closingEvents...)
	if appendErr == nil {
		s.projector.ResetSeq(currentSeq)
	}
	s.mu.Unlock()

	if appendErr != nil {
		return fmt.Errorf("append synthesized closing events: %w", appendErr)
	}

	s.triggerFlush()
	return nil
}
