package provider

// Anthropic request encoding: the Messages API wire body.
//
// Split out of anthropic.go, which was carrying the client, the request
// encoder, and the response decoder in one file. The client keeps
// anthropic.go; the decoder lives in anthropic_response.go. This mirrors the
// openai_compat_request.go split on the other client.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// buildRequestBody translates req into Anthropic's wire shape: system pulled
// to the top level, messages translated and role-coalesced (see
// anthropicSystemAndMessages), tools and tool_choice translated from the
// OpenAI shape, and the resolved reasoning dialect's fields merged in via the
// same reasoningBodyFields every other dialect uses (Anthropic's shape - a
// top-level thinking object plus a nested output_config.effort - is just
// another case in that switch, not a separate encoder).
func (c *AnthropicCompleter) buildRequestBody(req Request) (map[string]any, error) {
	// Every OpenAI-compatible provider gets these two repairs for free via
	// toAPIMessages (api_message.go), which this native client does not call
	// (it builds Anthropic's own wire shape directly, not an OpenAI-style
	// body). Applying them here closes the same gap: an empty assistant turn
	// (no content, no tool calls - the shape a genuinely empty provider
	// response leaves behind) would otherwise still open an "assistant"
	// pending turn in anthropicSystemAndMessages and then contribute zero
	// content blocks, silently causing the NEXT user/tool message to start a
	// fresh "user" turn instead of extending the one before the empty
	// assistant message - two adjacent Anthropic "user" messages, which 400s
	// on role alternation the same way the uncoalesced-tool-results bug did.
	// An orphaned tool_use/tool_result pair (a torn session write) is the
	// same class of poisoned-history shape and gets the same treatment.
	msgs := RepairToolPairing(DropEmptyAssistantTurns(req.Messages))
	system, messages, anchors := anthropicSystemAndMessages(msgs)
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to send")
	}
	dialect := req.ReasoningDialect
	if dialect == "" {
		dialect = c.reasoning
	}
	resolved := reasoning.Resolve(c.name, reasoning.Setting{Level: req.ReasoningLevel, Dialect: dialect})

	body := map[string]any{
		"model":      req.Model,
		"max_tokens": anthropicMaxTokens(req, resolved.Level),
		"messages":   messages,
	}
	if system != "" {
		body["system"] = system
	}
	if tools := anthropicTools(req.Tools); len(tools) > 0 {
		body["tools"] = tools
	}
	if choice := anthropicToolChoice(req.ToolChoice); choice != nil {
		body["tool_choice"] = choice
	}
	// req.Temperature is deliberately NEVER forwarded, unlike every other
	// dialect's request builder. Anthropic's claude-sonnet-5 (and the rest
	// of the current model generation) rejects a non-default temperature
	// outright - HTTP 400 - and this code has no way to tell "the caller's
	// value happens to equal Anthropic's default" from "the caller wants a
	// different value": Request.Temperature is a bare *float64 carrying
	// whatever a session/model-wide [chat] setting resolved to, not a
	// signal of intent specific to this provider. Omitting the field is
	// always safe (Anthropic runs at its own default) and is Anthropic's
	// own documented recommendation for steering behavior on models where
	// sampling parameters are removed - use effort/prompting instead. Step-5
	// bug audit caught an earlier version of this that forwarded the value
	// verbatim: with a config carrying a non-default temperature (e.g. the
	// bug report this feature exists to fix, [chat] temperature = 0.0),
	// every request 400s exactly as before, silently defeating the whole
	// native-Anthropic-routing fix.
	for k, v := range reasoningBodyFields(resolved.Dialect, resolved.Level) {
		body[k] = v
	}
	// Marking runs last so it sees the final system/messages values. When the
	// option is off nothing is touched and the body stays byte-identical to
	// the pre-marker layout.
	if c.cacheMarkers {
		markAnthropicCachePrefix(body, messages, anchors)
	}
	return body, nil
}

// markAnthropicCachePrefix places explicit cache_control breakpoints on the
// stable request prefix.
//
// Anthropic never caches implicitly: a request with no cache_control block is
// billed in full every time. In a multi-step tool loop that means re-paying
// for the entire transcript on every step, which is both the dominant cost of
// a long turn and the reason the per-step usage line reports no cache at all.
//
// Three of Anthropic's four permitted breakpoints are used, matching the
// policy the OpenAI-compatible client already applies through
// markStablePrefixCacheControl - one policy, two wire shapes, deliberately
// kept in step:
//
//   - the system prompt, which never changes within a session;
//   - the first user message, pinning the conversation's fixed head;
//   - a ROLLING breakpoint on the newest stable user turn, so the
//     append-only transcript behind it is cached instead of re-billed each
//     step. Moving the marker forward between steps is safe - cache_control
//     placement is excluded from prefix matching upstream.
//
// Assistant turns are never anchored: reasoning replay rewrites them, so they
// are not stable. Neither are host-injected ephemeral trailers, which is what
// the anchors slice tracks - see anthropicPendingTurn.stable.
func markAnthropicCachePrefix(body map[string]any, messages []map[string]any, anchors []int) {
	if system, ok := body["system"].(string); ok && system != "" {
		body["system"] = []any{map[string]any{
			"type":          "text",
			"text":          system,
			"cache_control": anthropicEphemeralCacheControl(),
		}}
	}
	firstUser := -1
	for i, msg := range messages {
		if msg["role"] == "user" {
			firstUser = i
			break
		}
	}
	if firstUser >= 0 {
		markAnthropicBlock(messages, anchors, firstUser)
	}
	for i := len(messages) - 1; i > firstUser; i-- {
		if messages[i]["role"] != "user" {
			continue
		}
		markAnthropicBlock(messages, anchors, i)
		return
	}
}

// markAnthropicBlock stamps the cache marker on message index's anchor block -
// the last block that will still be there, unchanged, next step. An index with
// no stable block (anchor < 0, a turn made only of ephemeral host text) is
// skipped rather than anchored on content that cannot cache.
func markAnthropicBlock(messages []map[string]any, anchors []int, index int) {
	if index < 0 || index >= len(anchors) {
		return
	}
	anchor := anchors[index]
	if anchor < 0 {
		return
	}
	blocks, ok := messages[index]["content"].([]map[string]any)
	if !ok || anchor >= len(blocks) {
		return
	}
	blocks[anchor]["cache_control"] = anthropicEphemeralCacheControl()
}

func anthropicEphemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

// anthropicMaxTokens picks the wire max_tokens: the caller's explicit value
// when set, otherwise a conservative per-effort-level floor. See the
// AnthropicCompleter doc comment - this heuristic is unverified against a
// live API and should be tuned once real traffic data exists.
func anthropicMaxTokens(req Request, level reasoning.Level) int {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return *req.MaxTokens
	}
	return reasoning.OutputReserveFloor(level)
}

// anthropicTools translates the OpenAI-shaped tools[] entries
// ({"type":"function","function":{"name","description","parameters"}}, see
// internal/tools.Registry.OpenAITools) into Anthropic's flatter
// {"name","description","input_schema"} shape. An entry missing the
// "function" wrapper or a name is skipped rather than sent malformed -
// defensive only; every caller in this tree builds tools through
// Registry.OpenAITools, which always includes both.
func anthropicTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		tool := map[string]any{"name": name}
		if desc, ok := fn["description"].(string); ok {
			tool["description"] = desc
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			tool["input_schema"] = params
		} else {
			tool["input_schema"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, tool)
	}
	return out
}

// anthropicToolChoice translates Request.ToolChoice ("auto", "none", or
// empty) into Anthropic's {"type": ...} shape. Empty returns nil (omit the
// field entirely, Anthropic's own default is "auto"). Any other value is
// passed through as {"type": value} rather than silently dropped, so a
// caller-set "any" or "tool" (values this codebase's Request.ToolChoice
// comment documents as OpenAI-only today) at least reaches the wire visibly
// instead of vanishing.
func anthropicToolChoice(choice string) map[string]any {
	switch choice {
	case "":
		return nil
	case "none":
		return map[string]any{"type": "none"}
	default:
		return map[string]any{"type": choice}
	}
}

// anthropicPendingTurn accumulates one Anthropic wire message's content
// blocks while anthropicSystemAndMessages walks the OpenAI-shaped history.
type anthropicPendingTurn struct {
	role    string
	content []map[string]any
	// stable is the number of leading content blocks that are safe to anchor
	// a cache breakpoint on: every block contributed by a source message that
	// will still be present, unchanged, in the next request. Host-injected
	// ephemeral trailers (a named user message - the context summary or a
	// conclude nudge) do not recur, so a breakpoint placed on one is a
	// guaranteed miss on the following step. See anthropicCacheAnchor.
	stable int
}

// anthropicSystemAndMessages translates an OpenAI-shaped message history into
// Anthropic's system string plus messages array.
//
// Anthropic requires the top-level messages array to strictly alternate
// role "user" / role "assistant", with every tool_result for one assistant
// turn living inside a single "user" message's content array. This
// codebase's history is one Message per wire turn (see
// mivia-ai-sdk/agentloop/toolcall.go's runToolCalls, which appends one
// RoleTool Message per parallel tool call), so a naive 1:1 translation would
// emit N consecutive Anthropic "user" messages for an N-call parallel tool
// turn - a guaranteed 400 ("messages: roles must alternate") on the very
// first multi-tool-call turn. The fix generalizes past just RoleTool: RoleTool
// and RoleUser both map to Anthropic role "user" (a tool result and a
// following user-authored or injected notice message, e.g. the
// prompt-too-long compaction notice in internal/agent/agentloop_run.go,
// which is deliberately RoleUser), so this function coalesces every run of
// consecutive Messages that map to the same Anthropic role into one wire
// message with multiple content blocks, in original order - not just
// consecutive RoleTool runs.
func anthropicSystemAndMessages(msgs []Message) (system string, out []map[string]any, anchors []int) {
	var systemParts []string
	var cur *anthropicPendingTurn
	flush := func() {
		if cur != nil && len(cur.content) > 0 {
			out = append(out, map[string]any{"role": cur.role, "content": cur.content})
			anchors = append(anchors, cur.stable-1)
		}
		cur = nil
	}
	openTurn := func(role string) {
		if cur == nil || cur.role != role {
			flush()
			cur = &anthropicPendingTurn{role: role}
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, m.Content)
			}
		case RoleTool:
			openTurn("user")
			cur.content = append(cur.content, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})
			cur.stable = len(cur.content)
		case RoleUser:
			openTurn("user")
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
				if m.Name == "" {
					cur.stable = len(cur.content)
				}
			}
		case RoleAssistant:
			openTurn("assistant")
			// No thinking block is replayed here - see
			// anthropicThinkingDisplayText's doc comment for why history
			// only carries display text, not the signed block Anthropic
			// would need to replay it unmodified. Whether omitting the
			// thinking block is actually safe on a turn that also carries
			// ToolCalls, immediately followed by a RoleTool message (the
			// common agentic shape - reasoning + tool call in one turn,
			// continued by its tool_result) was a long-standing open
			// question; a live manual test (claude-sonnet-5 via
			// llmproxycli's DialectAnthropicAdaptive, reasoning=high, two
			// sequential tool calls each continued by its tool_result,
			// 2026-08-29) completed with no error. The design doc's §3.5
			// example still shows a thinking block replayed alongside
			// tool_use as the normative shape; this code takes the opposite
			// path (omit rather than reconstruct-unsigned) because sending
			// a signature that doesn't match reconstructed content is the
			// failure mode Anthropic is documented to reject. The live test
			// is one manual session, not an automated regression harness -
			// see TestAnthropicReplayOfToolCallTurnWithReasoning, which
			// still pins this as current behavior rather than asserting it
			// is correct for every shape (long thinking traces, interleaved
			// thinking, and redacted_thinking remain unexercised).
			if strings.TrimSpace(m.Content) != "" {
				cur.content = append(cur.content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				cur.content = append(cur.content, anthropicToolUseBlock(tc))
			}
		}
	}
	flush()
	return strings.Join(systemParts, "\n\n"), out, anchors
}

// anthropicToolUseBlock builds one tool_use content block from an
// OpenAI-shaped ToolCall, preserving its ID verbatim (Anthropic requires a
// later tool_result.tool_use_id to exactly match). Arguments is a JSON-encoded
// string on the OpenAI shape; Anthropic's "input" is the decoded object
// itself, not a string.
func anthropicToolUseBlock(tc ToolCall) map[string]any {
	var input any
	if strings.TrimSpace(tc.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = nil
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	return map[string]any{
		"type":  "tool_use",
		"id":    tc.ID,
		"name":  tc.Function.Name,
		"input": input,
	}
}
