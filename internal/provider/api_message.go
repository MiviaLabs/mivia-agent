package provider

import "strings"

// apiMessage is the wire shape. It exists so host-only fields on Message
// (CreatedAt) cannot reach the API: `omitempty` does not suppress a zero
// time.Time, so zeroing the field still encoded created_at:"0001-01-01…".
type apiMessage struct {
	Role string `json:"role"`
	// Content is a pointer so "absent" and "present but empty" stay distinct:
	// omitempty drops a nil pointer but keeps a pointer to "". A tool result is
	// legitimately empty (reading a zero-byte file) and the API still requires
	// the field, whereas an assistant turn that only calls tools must omit it.
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// toAPIMessages converts host history to the wire shape.
//
// It drops assistant turns carrying neither content nor tool calls. Such a
// message encodes to a bare {"role":"assistant"} and OpenAI-compatible APIs
// reject the whole request with HTTP 400 ("content or tool calls must be
// provided"). Because history is replayed on every turn, one of them makes a
// session permanently unusable — and it is persisted, so restarting does not
// help. Dropping here repairs sessions already on disk.
//
// An assistant turn with tool calls and no content is legitimate and kept: the
// tool results that follow reference its tool_call_id, and only there is the
// content field omitted rather than sent empty.
func toAPIMessages(msgs []Message) []apiMessage {
	msgs = RepairToolPairing(msgs)
	out := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleAssistant && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) == "" {
			continue
		}
		am := apiMessage{
			Role:       m.Role,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		// Omit content only for a pure tool-call turn; every other role must
		// carry the field even when the value is the empty string.
		if !(m.Role == RoleAssistant && len(m.ToolCalls) > 0 && m.Content == "") {
			content := m.Content
			am.Content = &content
		}
		out = append(out, am)
	}
	return out
}

// RepairToolPairing drops the message shapes an API rejects outright: an
// assistant tool_call with no matching tool result, and a tool result naming a
// call nobody announced.
//
// Either shape poisons a session permanently — history is replayed on every
// turn, so the request keeps being rejected and no UI action recovers it. They
// arise from a torn session write (chunks are rewritten in place, and a reader
// stops at the last complete line without error) and from any producer that
// records a partial turn.
//
// Repairing here rather than only at the source heals histories already on
// disk, whatever wrote them.
func RepairToolPairing(msgs []Message) []Message {
	answered := make(map[string]bool)
	announced := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == RoleTool && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
		for _, c := range m.ToolCalls {
			announced[c.ID] = true
		}
	}
	needsWork := false
	for id := range announced {
		if !answered[id] {
			needsWork = true
		}
	}
	for id := range answered {
		if !announced[id] {
			needsWork = true
		}
	}
	if !needsWork {
		return msgs
	}

	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == RoleTool {
			if m.ToolCallID == "" || !announced[m.ToolCallID] {
				continue // orphaned result
			}
			out = append(out, m)
			continue
		}
		if len(m.ToolCalls) > 0 {
			kept := make([]ToolCall, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				if answered[c.ID] {
					kept = append(kept, c)
				}
			}
			if len(kept) == 0 && strings.TrimSpace(m.Content) == "" {
				continue // nothing left to say and nothing left to call
			}
			m.ToolCalls = kept
		}
		out = append(out, m)
	}
	return out
}
