package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ChatStream implements Completer. It streams the same way ChatTurn does
// when given a StreamWriter (see ChatTurn's doc comment) and discards
// everything but the final text - the shape every other provider's
// ChatStream already has for the no-tools case.
func (c *AnthropicCompleter) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
	req.Stream = true
	req.StreamWriter = w
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

// chatTurnStream sends body with stream:true, decodes Anthropic's SSE
// event stream, writes each text_delta's text to req.StreamWriter as it
// arrives, and returns the accumulated *Response once the stream ends -
// the same shape ChatTurn's non-stream path returns, so callers cannot tell
// which path served a given turn except by latency.
func (c *AnthropicCompleter) chatTurnStream(ctx context.Context, req Request, body map[string]any) (*Response, error) {
	streamBody := make(map[string]any, len(body)+1)
	for k, v := range body {
		streamBody[k] = v
	}
	streamBody["stream"] = true

	httpReq, cancel, err := c.newHTTPRequest(ctx, req, streamBody)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, asTransient(fmt.Errorf("%s: %w", c.name, markTransientReadDeadline(ctx, req.Timeout, err)))
	}
	defer func() { _ = resp.Body.Close() }()

	// Both reads below are watchdog-bounded: a stream that opens and then
	// stops feeding, and an error response whose explanation never arrives,
	// are the same hazard - a body read with no bound of its own.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(wrapBodyWithIdleWatchdog(resp.Body, c.name), maxJSONResponseBytes))
		return nil, anthropicErrorFromBody(c.name, resp.StatusCode, raw)
	}
	return decodeAnthropicStream(wrapBodyWithIdleWatchdog(resp.Body, c.name), req.StreamWriter)
}

// anthropicStreamBlock accumulates one content block's deltas across the SSE
// stream, indexed by Anthropic's per-event "index" field. Anthropic
// guarantees a block's deltas arrive in order between its
// content_block_start and content_block_stop, so simple string
// concatenation (rather than a byte-offset-indexed buffer) is correct.
type anthropicStreamBlock struct {
	blockType string
	text      strings.Builder // text_delta (type "text") or thinking_delta (type "thinking")
	signature strings.Builder // signature_delta, thinking blocks only - see the open question in anthropic.go's doc comment
	jsonInput strings.Builder // input_json_delta (type "tool_use")
	id        string
	name      string
}

// decodeAnthropicStream reads one SSE response body to completion, writing
// text deltas to w as they arrive and returning the accumulated Response.
// Anthropic's stream is a strict superset of what this client needs to
// track: message_start/content_block_start/content_block_delta/
// content_block_stop carry the content, message_delta carries the final
// stop_reason and output usage, message_stop ends the stream.
//
// An in-band error (an "error" event, or a malformed data payload) ends the
// read and returns what has been decoded as an error - never a panic, and
// never silently truncated output presented as a complete answer, mirroring
// openai_compat_stream.go's convention of surfacing mid-stream failure as an
// error rather than a partial success.
func decodeAnthropicStream(body io.Reader, w io.Writer) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	blocks := map[int]*anthropicStreamBlock{}
	var blockOrder []int
	var stopReason string
	usage := anthropicStreamUsage{}

	var eventName string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			done, err := applyAnthropicStreamEvent(eventName, []byte(data), blocks, &blockOrder, &stopReason, &usage, w)
			if err != nil {
				return nil, fmt.Errorf("anthropic: stream: %w", err)
			}
			if done {
				return finishAnthropicStream(blocks, blockOrder, stopReason, usage), nil
			}
		case line == "":
			eventName = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("anthropic: stream: %w", err)
	}
	// The stream closed without a message_stop event. Anthropic always sends
	// one on a clean end; treat an early close as the (possibly partial)
	// answer rather than an error, the same tolerant convention
	// openai_compat_stream.go applies to a connection that closes right
	// after its last usable chunk.
	//
	// That tolerance stops at a tool call. Partial TEXT is still usable
	// prose, but partial ARGUMENTS are an instruction the model never
	// finished giving, and executing them is not a degraded answer - it is a
	// different action.
	if err := anthropicTruncatedToolCallError(blocks, blockOrder); err != nil {
		return nil, err
	}
	return finishAnthropicStream(blocks, blockOrder, stopReason, usage), nil
}

// anthropicTruncatedToolCallError refuses a torn stream that ended inside a
// tool call, mirroring validateTruncatedToolCalls on the OpenAI-compatible
// reader. It runs ONLY on the no-message_stop path: a completed stream has
// said its tool calls are whole.
//
// Empty arguments are rejected here, where the sibling reader permits them,
// because Anthropic carries a tool call's arguments as input_json_delta
// events. On a stream that was cut, an empty accumulator means the arguments
// never arrived - indistinguishable from a tool that takes none - and
// finishAnthropicStream's "{}" substitution would turn that into a call the
// model never made. Only message_stop makes the substitution sound.
//
// The failure is transient: a torn connection never delivered an answer, so
// the caller may ask again rather than acting on a fragment.
func anthropicTruncatedToolCallError(blocks map[int]*anthropicStreamBlock, order []int) error {
	for _, idx := range order {
		block := blocks[idx]
		if block == nil || block.blockType != "tool_use" {
			continue
		}
		if block.id == "" || block.name == "" {
			return &TransientError{Err: fmt.Errorf("anthropic: stream ended without a completion signal (truncated tool call)")}
		}
		args := strings.TrimSpace(block.jsonInput.String())
		if args == "" || !json.Valid([]byte(args)) {
			return &TransientError{Err: fmt.Errorf("anthropic: stream ended without a completion signal (tool call %q has incomplete arguments)", block.id)}
		}
	}
	return nil
}

// anthropicStreamEventEnvelope peeks the "type" field every SSE data payload
// carries, before dispatching to the shape that event type actually has.
type anthropicStreamEventEnvelope struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// applyAnthropicStreamEvent applies one decoded SSE event to the in-progress
// accumulators. Returns done=true once message_stop is seen (or an
// in-band "error" event arrives, which is surfaced as err rather than
// silently ending the stream).
func applyAnthropicStreamEvent(eventName string, data []byte, blocks map[int]*anthropicStreamBlock, blockOrder *[]int, stopReason *string, usage *anthropicStreamUsage, w io.Writer) (done bool, err error) {
	var head anthropicStreamEventEnvelope
	if err := json.Unmarshal(data, &head); err != nil {
		return false, fmt.Errorf("decode event: %w", err)
	}
	eventType := head.Type
	if eventType == "" {
		eventType = eventName
	}
	switch eventType {
	case "message_start":
		return false, applyAnthropicMessageStart(data, usage)
	case "content_block_start":
		return false, applyAnthropicContentBlockStart(data, blocks, blockOrder)
	case "content_block_delta":
		return false, applyAnthropicContentBlockDelta(data, blocks, w)
	case "content_block_stop":
		// Nothing to do: the block's accumulated content is read from
		// `blocks` in finishAnthropicStream once the stream ends.
		return false, nil
	case "message_delta":
		return false, applyAnthropicMessageDelta(data, stopReason, usage)
	case "message_stop":
		return true, nil
	case "error":
		return false, anthropicInBandStreamError(data)
	case "ping":
		// Keepalive; nothing to accumulate.
		return false, nil
	default:
		return false, nil
	}
}

// anthropicStreamUsage accumulates one streamed message's usage across the
// two events that carry it: message_start supplies every prompt-side field
// (including the cache counters), message_delta supplies output_tokens. The
// raw wire struct is kept rather than a pre-summed TokenUsage so the cache
// counters survive to finishAnthropicStream, which needs them for both the
// true prompt total and the CacheUsage report - the non-stream path derives
// both from the same anthropicUsage methods.
type anthropicStreamUsage struct {
	wire     anthropicUsage
	reported bool
}

func (u anthropicStreamUsage) tokenUsage() TokenUsage {
	if !u.reported {
		return TokenUsage{}
	}
	return TokenUsage{
		Reported:     true,
		InputTokens:  u.wire.promptTokens(),
		OutputTokens: nonNegative(u.wire.OutputTokens),
	}
}

func applyAnthropicMessageStart(data []byte, usage *anthropicStreamUsage) error {
	var msg struct {
		Message struct {
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &msg) == nil {
		// output_tokens is not final at message_start; message_delta
		// overwrites it below. Every other field is prompt-side and set once.
		outputSoFar := usage.wire.OutputTokens
		usage.wire = msg.Message.Usage
		usage.wire.OutputTokens = outputSoFar
		usage.reported = true
	}
	return nil
}

func applyAnthropicContentBlockStart(data []byte, blocks map[int]*anthropicStreamBlock, blockOrder *[]int) error {
	var ev struct {
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("decode content_block_start: %w", err)
	}
	blocks[ev.Index] = &anthropicStreamBlock{
		blockType: ev.ContentBlock.Type,
		id:        ev.ContentBlock.ID,
		name:      ev.ContentBlock.Name,
	}
	*blockOrder = append(*blockOrder, ev.Index)
	return nil
}

func applyAnthropicContentBlockDelta(data []byte, blocks map[int]*anthropicStreamBlock, w io.Writer) error {
	var ev struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("decode content_block_delta: %w", err)
	}
	block := blocks[ev.Index]
	if block == nil {
		return nil
	}
	switch ev.Delta.Type {
	case "text_delta":
		block.text.WriteString(ev.Delta.Text)
		if w != nil && ev.Delta.Text != "" {
			_, _ = io.WriteString(w, ev.Delta.Text)
		}
	case "thinking_delta":
		block.text.WriteString(ev.Delta.Thinking)
	case "signature_delta":
		block.signature.WriteString(ev.Delta.Signature)
	case "input_json_delta":
		block.jsonInput.WriteString(ev.Delta.PartialJSON)
	}
	return nil
}

func applyAnthropicMessageDelta(data []byte, stopReason *string, usage *anthropicStreamUsage) error {
	var ev struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return fmt.Errorf("decode message_delta: %w", err)
	}
	if ev.Delta.StopReason != "" {
		*stopReason = ev.Delta.StopReason
	}
	usage.wire.OutputTokens = ev.Usage.OutputTokens
	usage.reported = true
	return nil
}

// anthropicInBandStreamError decodes an in-band SSE "error" event into a Go
// error that ends the stream read (see decodeAnthropicStream), rather than
// silently truncating the answer and presenting it as complete.
func anthropicInBandStreamError(data []byte) error {
	var ev struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &ev)
	return fmt.Errorf("in-band error event (type %s)", ev.Error.Type)
}

// finishAnthropicStream assembles the final Response from the accumulated
// per-block state, in the order blocks were opened - the same shape
// anthropicResponseToProvider produces for the non-stream path, so a caller
// cannot tell which path served a given turn from the Response alone.
func finishAnthropicStream(blocks map[int]*anthropicStreamBlock, order []int, stopReason string, usage anthropicStreamUsage) *Response {
	var textParts []string
	var toolCalls []ToolCall
	var thinkingBlocks []json.RawMessage
	for _, idx := range order {
		block := blocks[idx]
		if block == nil {
			continue
		}
		switch block.blockType {
		case "text":
			if s := block.text.String(); s != "" {
				textParts = append(textParts, s)
			}
		case "tool_use":
			args := block.jsonInput.String()
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			tc := ToolCall{ID: block.id, Type: "function"}
			tc.Function.Name = block.name
			tc.Function.Arguments = args
			toolCalls = append(toolCalls, tc)
		case "thinking":
			raw := anthropicThinkingBlockJSON(block)
			if raw != nil {
				thinkingBlocks = append(thinkingBlocks, raw)
			}
		}
	}
	return &Response{
		Content:          strings.Join(textParts, ""),
		ReasoningContent: anthropicThinkingDisplayText(thinkingBlocks),
		ToolCalls:        toolCalls,
		FinishReason:     anthropicFinishReason(stopReason),
		TokenUsage:       usage.tokenUsage(),
		CacheUsage:       usage.wire.cacheUsage(),
	}
}

// anthropicThinkingBlockJSON re-serializes one streamed thinking block into
// the same {"type":"thinking","thinking":"...","signature":"..."} shape the
// non-stream path would have received directly, so
// anthropicThinkingDisplayText (which only reads the "thinking" field) sees
// an identical shape regardless of which path produced it. The signature
// field is included when present, even though nothing currently reads it
// back out - see anthropic.go's package doc comment on why thinking blocks
// are not replayed byte-for-byte.
func anthropicThinkingBlockJSON(block *anthropicStreamBlock) json.RawMessage {
	obj := map[string]any{"type": "thinking", "thinking": block.text.String()}
	if sig := block.signature.String(); sig != "" {
		obj["signature"] = sig
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return raw
}
