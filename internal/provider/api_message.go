package provider

import "strings"

// ReasoningElisionMarker is the host-authored placeholder that replaces a
// prior turn's assistant chain-of-thought when context compaction elides it
// (internal/contextmgr owns the elision; this package owns the constant
// because this package decides what reaches the wire).
//
// It exists as a non-empty string for exactly one reason: a provider with
// RejectReasoningLessToolTurns (DeepSeek's documented 400) drops an assistant
// tool-call turn with empty reasoning TOGETHER with its tool results, so an
// elided-to-empty exchange would fall out of history entirely. That reason
// does not apply to a replay-only provider, and for those the marker must NOT
// reach the wire - see toAPIMessages.
const ReasoningElisionMarker = "[reasoning elided by context compaction]"

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
	// ReasoningContent is emitted only when the client declared
	// RequiresReasoningReplay and the host message is an assistant turn with
	// non-empty chain-of-thought. omitempty keeps non-adopting request bodies
	// byte-identical. The wire field is protocol-bound and intentionally
	// unredacted: providers that require replay demand the verbatim echo of
	// the reasoning they produced, so redaction happens at operator-facing
	// sinks (events, persistence, checkpoints) and never on this request path.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// toAPIMessages converts host history to the wire shape.
//
// It drops assistant turns carrying neither content nor tool calls. Such a
// message encodes to a bare {"role":"assistant"} and OpenAI-compatible APIs
// reject the whole request with HTTP 400 ("content or tool calls must be
// provided"). Because history is replayed on every turn, one of them makes a
// session permanently unusable - and it is persisted, so restarting does not
// help. Dropping here repairs sessions already on disk.
//
// An assistant turn with tool calls and no content is legitimate and kept: the
// tool results that follow reference its tool_call_id, and only there is the
// content field omitted rather than sent empty.
//
// When rejectReasoningLess is true (DeepSeek documented-400 gate), assistant
// tool-call turns with empty ReasoningContent are dropped together with their
// tool results (D2). z.ai sets replay without reject so reasoning=off tool
// turns still ship. Reasoning is copied onto the wire only when
// replayReasoning is on and the assistant value is non-empty.
func toAPIMessages(msgs []Message, replayReasoning, rejectReasoningLess bool) []apiMessage {
	msgs = RepairToolPairing(msgs)
	if rejectReasoningLess {
		// Backstop only: the primary repair runs once at turn-adoption time
		// (chat.finishAgentTurn -> RepairReasoningLessToolExchanges), so a
		// correctly persisted history hits this as a no-op. This still
		// protects sessions persisted before that repair existed, and any
		// caller that serializes without going through finishAgentTurn.
		msgs = RepairReasoningLessToolExchanges(msgs)
	}
	out := make([]apiMessage, 0, len(msgs))
	for _, m := range msgs {
		if isEmptyAssistantTurn(m) {
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
		if replayReasoning && m.Role == RoleAssistant && replayableReasoning(m.ReasoningContent, rejectReasoningLess) {
			am.ReasoningContent = m.ReasoningContent
		}
		out = append(out, am)
	}
	return out
}

// replayableReasoning reports whether an assistant turn's chain-of-thought
// may be echoed back to a replay-requiring provider.
//
// The elision marker is host prose, not model output. Echoing it into
// reasoning_content tells the model it once thought a sentence it never
// thought - and for a preserved-thinking dialect (z.ai's GLM family, which
// sends clear_thinking:false so prior thinking stays in scope) that fabricated
// block is fed straight back into the model's own reasoning context. The
// honest wire shape is no reasoning field at all: the turn's reasoning is
// genuinely gone, and every replay-only provider (z.ai) documents missing
// reasoning as degradation, never as a rejection.
//
// A reject-reasoning-less provider (DeepSeek) is the exception and keeps the
// marker: for it, an assistant tool-call turn whose wire message carries no
// reasoning_content is a documented 400, so a fabricated block is strictly
// better than a failed request. See ReasoningElisionMarker.
func replayableReasoning(reasoningContent string, rejectReasoningLess bool) bool {
	if reasoningContent == "" {
		return false
	}
	return rejectReasoningLess || reasoningContent != ReasoningElisionMarker
}

// RepairReasoningLessToolExchanges removes assistant tool-call turns that
// lack reasoning_content, together with their tool results. Used only when
// RejectReasoningLessToolTurns is set (DeepSeek): those turns 400 on a
// tools-carrying request. Non-tool assistant turns without reasoning are kept.
//
// This is the documented repair for DeepSeek's 400 gate, and it costs
// context: an older reasoning-less exchange is dropped WITH its results, so
// only the terminal exchange (the current loop's pending call plus its
// results) survives. The tradeoff is accepted because the alternative is a
// session the API rejects on every later turn.
//
// The gate is unconditional for every DeepSeek client regardless of
// configured reasoning effort - it is NOT limited to any particular
// thinking_effort value. Do not assume a shipped config avoids this path.
// Because dropping here is per-serialization, a caller that re-derives the
// wire body on every request (toAPIMessages) would otherwise silently
// re-rewrite history every time it is asked, breaking the provider's
// prompt-cache prefix and hiding context with no persisted trace. The primary
// call site is chat.finishAgentTurn, which runs this once at turn-adoption
// time so persisted history is already valid; toAPIMessages's own call is a
// defensive no-op backstop, not the source of truth.
func RepairReasoningLessToolExchanges(msgs []Message) []Message {
	dropIDs := map[string]struct{}{}
	for i, m := range msgs {
		if m.Role != RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		if strings.TrimSpace(m.ReasoningContent) != "" {
			continue
		}
		// Preserve the exchange just produced by the current loop. Dropping
		// it would also drop its tool result, so the model would see no result
		// and could issue the same call forever. Older exchanges are repaired.
		if terminalToolExchange(msgs, i) {
			continue
		}
		for _, c := range m.ToolCalls {
			if c.ID != "" {
				dropIDs[c.ID] = struct{}{}
			}
		}
	}
	if len(dropIDs) == 0 {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for i, m := range msgs {
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 && strings.TrimSpace(m.ReasoningContent) == "" && !terminalToolExchange(msgs, i) {
			continue
		}
		if m.Role == RoleTool {
			if _, drop := dropIDs[m.ToolCallID]; drop {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// isEmptyAssistantTurn reports whether m is an assistant message carrying
// neither content, tool calls, nor reasoning content - the shape a
// provider's genuinely empty response leaves behind. Shared by
// toAPIMessages (the wire-layer drop) and DropEmptyAssistantTurns (the
// message-list-layer drop) so the two definitions cannot silently drift
// apart.
//
// ReasoningContent is checked because it is not always a replay-only
// passenger riding alongside real Content or ToolCalls: the native Anthropic
// client (anthropic.go) can produce a Response whose only content is a
// thinking block - Content and ToolCalls both empty, ReasoningContent
// carrying the raw thinking-block JSON - when a turn's max_tokens budget
// runs out mid-thought (stop_reason "max_tokens") before any text or
// tool_use block starts. Treating that shape as "empty" would silently drop
// a legitimate assistant turn (and the model's reasoning trace with it) the
// same way an actually-empty response should be dropped - a correctness bug
// found in Step-5 bug audit, not a hypothetical.
func isEmptyAssistantTurn(m Message) bool {
	return m.Role == RoleAssistant && len(m.ToolCalls) == 0 &&
		strings.TrimSpace(m.Content) == "" && strings.TrimSpace(m.ReasoningContent) == ""
}

// DropEmptyAssistantTurns removes assistant messages that carry neither
// content nor tool calls - the shape a provider's genuinely empty response
// leaves behind. toAPIMessages already drops this shape at the wire layer
// (its own doc comment: such a message "makes a session permanently
// unusable" once persisted, because it is replayed on every later turn and
// OpenAI-compatible APIs 400 on it) - but that repair only protects the
// provider request, not the message list itself. ValidateToolPairing (used
// by contextmgr's planner to gate every Prepare()/commit) hard-rejects the
// identical shape instead of silently dropping it, so a persisted session
// carrying this message fails EVERY subsequent turn's context preparation,
// not just the wire request. This is the same repair, applied to the
// message list itself so the shape is gone before it is ever validated or
// persisted, not just before it is serialized.
func DropEmptyAssistantTurns(msgs []Message) []Message {
	needsWork := false
	for _, m := range msgs {
		if isEmptyAssistantTurn(m) {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return msgs
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if isEmptyAssistantTurn(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// terminalToolExchange identifies the current loop's pending exchange: the
// assistant tool-call message is followed only by its matching tool results,
// ignoring trailing host injections.
//
// The host appends messages AFTER the current exchange's tool results before
// the request ships: the user-role context summary (internal/agent
// RenderSummaryMessage) and the conclude nudge. Those trailing messages are
// not part of the exchange, so the trim below drops them from the check.
// Without the trim, an appended summary made the current exchange
// non-terminal and RepairReasoningLessToolExchanges removed it WITH its tool
// results - the model never saw the result it just produced.
//
// Only trailing NAMED user messages are trimmed. Host injections always
// carry a Name (agent.SummaryMessageName, agent.ConcludeNudgeMessageName)
// and user-typed input never does, so an un-named trailing user message is a
// real follow-up request: the exchange before it was already answered, stays
// non-terminal, and remains droppable (the D2 tradeoff pinned by
// TestReasoningReplayIntegrationLegacyExchangeDropped). A trailing assistant
// message likewise means the loop moved past the exchange.
func terminalToolExchange(msgs []Message, assistantIndex int) bool {
	ids := make(map[string]struct{})
	for _, call := range msgs[assistantIndex].ToolCalls {
		ids[call.ID] = struct{}{}
	}
	if len(ids) == 0 {
		return false
	}
	end := len(msgs)
	for end > assistantIndex+1 && msgs[end-1].Role == RoleUser && msgs[end-1].Name != "" {
		end--
	}
	seen := make(map[string]struct{})
	for _, m := range msgs[assistantIndex+1 : end] {
		if m.Role != RoleTool {
			return false
		}
		if _, ok := ids[m.ToolCallID]; !ok {
			return false
		}
		seen[m.ToolCallID] = struct{}{}
	}
	return len(seen) == len(ids)
}

// RepairToolPairing drops the message shapes an API rejects outright: an
// assistant tool_call with no matching tool result, and a tool result naming a
// call nobody announced.
//
// Either shape poisons a session permanently - history is replayed on every
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
