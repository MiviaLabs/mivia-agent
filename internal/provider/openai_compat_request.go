package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// newRequest builds one /chat/completions HTTP request. It never mutates req.
func (c *OpenAICompat) newRequest(ctx context.Context, req Request) (*http.Request, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("%s: model is required", c.name)
	}
	if err := c.checkReservedExtras(); err != nil {
		return nil, err
	}
	raw, err := c.marshalBody(req)
	if err != nil {
		return nil, err
	}
	if req.DisableProviderReplay {
		ctx = context.WithValue(ctx, disableProviderReplayContextKey{}, true)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq, req, raw)
	return httpReq, nil
}

// checkReservedExtras refuses operator-supplied extras that would overwrite
// fields this client owns. The cache-marker keys are reserved too: this
// client emits cache_control on the stable prefix when CacheMarkersEnabled,
// so an extraBody smuggling its own marker (or a prompt_cache_* alias some
// OpenAI-compatible dialects honor) could inject a conflicting or
// unvetted cache instruction onto the wire.
func (c *OpenAICompat) checkReservedExtras() error {
	for key := range c.extraHeaders {
		for _, reserved := range []string{"Authorization", "Content-Type", "Accept", "Idempotency-Key"} {
			if strings.EqualFold(key, reserved) {
				return fmt.Errorf("%s: extra header %q is reserved", c.name, key)
			}
		}
	}
	for key := range c.extraBody {
		switch key {
		case "model", "messages", "stream", "cache_control", "prompt_cache_breakpoint", "prompt_cache_options":
			return fmt.Errorf("%s: extra body field %q is reserved", c.name, key)
		}
	}
	return nil
}

// marshalBody serializes the request body. The typed payload is round-tripped
// through a map so operator extras and reasoning fields can be merged by key.
//
// Merge order is the contract: typed payload, then ExtraBody, then reasoning.
// An active model-scoped level is a deliberate per-model instruction and
// outranks a static extra_body key naming the same field; without a stated
// order that would be a coin flip decided by map iteration. When no level is
// active, reasoning writes nothing, so extra_body remains the untouched escape
// hatch it has always been and the body stays byte-identical to a
// pre-reasoning one.
func (c *OpenAICompat) marshalBody(req Request) ([]byte, error) {
	payload := chatRequestBody{
		Model:       req.Model,
		Messages:    toAPIMessages(req.Messages, c.replayReasoning, c.rejectReasoningLessToolTurns),
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
	}
	if req.ToolChoice != "" {
		payload.ToolChoice = req.ToolChoice
	} else if len(req.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// raw is the marshalled form of a struct, so it is always a JSON object and
	// this decode cannot fail. The round-trip is kept even when there is
	// nothing to merge: re-marshalling the map sorts keys, and skipping it for
	// the no-extras case would change the serialized layout of every request
	// that exists today.
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	for key, value := range c.extraBody {
		body[key] = value
	}
	for key, value := range c.reasoningFields(req) {
		body[key] = value
	}
	// Dialects that replay assistant reasoning under a non-default wire field
	// (e.g. OpenRouter's "reasoning") get the key renamed here. The gate is
	// deliberately narrow: replay off, an empty field, or the legacy field
	// itself all leave the merged map untouched, so non-adopting request
	// bodies stay byte-identical to the pre-rename layout.
	if c.replayReasoning && c.replayReasoningField != "" && c.replayReasoningField != "reasoning_content" {
		renameReasoningContentKey(body, c.replayReasoningField)
	}
	// A client with cache markers enabled emits an explicit Anthropic-style
	// cache_control marker on the stable prefix. The gate is narrow: when the
	// option is off nothing is touched, so non-adopting request bodies stay
	// byte-identical to the pre-marker layout.
	if c.cacheMarkersEnabled {
		markStablePrefixCacheControl(body)
	}
	return json.Marshal(body)
}

// markStablePrefixCacheControl rewrites the cacheable prefix from plain
// string content to Anthropic-style content parts carrying an explicit cache
// marker: exactly [{"type":"text","text":<content>,"cache_control":
// {"type":"ephemeral"}}]. Three breakpoints are placed (within Anthropic's
// budget of 4): every system message, the FIRST user message, and a ROLLING
// breakpoint on the newest user or tool message - in a multi-step tool loop
// everything up to that message is an immutable, append-only transcript, and
// the rolling marker lets the provider cache it instead of re-billing the
// full history each step. The marker's forward movement between steps is
// safe: cache_control placement is excluded from provider prefix matching,
// and a plain string and a single text part normalize to the same upstream
// text block. Assistant turns are never marked - reasoning replay rewrites
// them, so they are not a stable anchor. It runs on the decoded map, so only
// string contents are converted and already-part content is left alone. Only
// the CacheMarkersEnabled gate in marshalBody ever calls it.
func markStablePrefixCacheControl(body map[string]any) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	// maxCacheBreakpoints is Anthropic's hard budget; a request carrying more
	// explicit markers is rejected with a 400. The single-system invariant
	// keeps real requests at 3, but the cap fails safe if that ever drifts.
	const maxCacheBreakpoints = 4
	marked := 0
	firstUserIndex := -1
	for i, entry := range messages {
		msg, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if marked >= maxCacheBreakpoints-1 {
			break
		}
		switch msg["role"] {
		case "system":
			if markMessageContent(msg) {
				marked++
			}
		case "user":
			if firstUserIndex < 0 {
				if markMessageContent(msg) {
					marked++
				}
				firstUserIndex = i
			}
		}
	}
	markRollingBreakpoint(messages, firstUserIndex)
}

// markRollingBreakpoint marks the newest user or tool message, walking
// backward past any trailing assistant turn. The first user message is
// skipped when it is also the newest - it already carries its fixed marker.
func markRollingBreakpoint(messages []any, firstUserIndex int) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		switch msg["role"] {
		case "user", "tool":
			if i != firstUserIndex {
				markMessageContent(msg)
			}
			return
		}
	}
}

// markMessageContent converts one message's string content into a single text
// content part carrying the ephemeral cache marker and reports whether it
// marked. Non-string content (or an absent content field) is left untouched.
// Empty content is left as a plain string: a legitimately empty tool result
// (reading a zero-byte file) wrapped as an empty text part is rejected by
// Anthropic with a 400, and an empty block is never worth a breakpoint.
func markMessageContent(msg map[string]any) bool {
	content, ok := msg["content"].(string)
	if !ok || content == "" {
		return false
	}
	msg["content"] = []any{map[string]any{
		"type":          "text",
		"text":          content,
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
	return true
}

// renameReasoningContentKey rewrites replayed assistant reasoning inside the
// merged request map from the legacy "reasoning_content" key to the dialect's
// field name, preserving the value. It runs on the decoded map, so messages
// without a reasoning key are untouched, and only the post-pass gate above
// ever calls it.
func renameReasoningContentKey(body map[string]any, field string) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	for _, entry := range messages {
		msg, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// Defense-in-depth: reasoning_content is only ever emitted on
		// assistant messages by toAPIMessages; skip anything else so a
		// future extraBody injection cannot be renamed into the new key.
		if msg["role"] != "assistant" {
			continue
		}
		value, present := msg["reasoning_content"]
		if !present {
			continue
		}
		delete(msg, "reasoning_content")
		msg[field] = value
	}
}

func (c *OpenAICompat) setHeaders(httpReq *http.Request, req Request, raw []byte) {
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("Accept", "application/json")
	// The Go transport can replay a request with an Idempotency-Key after a
	// reused connection fails. Panel actors forbid every transport replay.
	if !req.DisableProviderReplay {
		key := sha256.Sum256(raw)
		httpReq.Header.Set("Idempotency-Key", fmt.Sprintf("mivia-%d-%x", c.requestSeq.Add(1), key[:]))
	}
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if c.httpReferer != "" {
		httpReq.Header.Set("HTTP-Referer", c.httpReferer)
	}
	if c.xTitle != "" {
		httpReq.Header.Set("X-Title", c.xTitle)
	}
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
}
