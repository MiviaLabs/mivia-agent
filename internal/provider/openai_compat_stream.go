package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// chatTurnStream runs a streaming chat.completions request, forwarding text
// deltas to StreamWriter and assembling tool_calls from stream fragments.
func (c *OpenAICompat) chatTurnStream(ctx context.Context, req Request) (*Response, error) {
	callCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	req.Stream = true
	httpReq, err := c.newRequest(callCtx, req)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, asTransient(fmt.Errorf("%s: request failed: %w", c.name, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.httpError(resp)
	}

	content, reasoning, webSearch, toolCalls, finishReason, received, usage, err := c.readTurnStream(callCtx, resp.Body, req.StreamWriter, req.Timeout)
	if err != nil {
		return nil, err
	}
	// Empty stream with no tools → fall back to non-stream once.
	if !received {
		if req.DisableProviderReplay {
			return nil, fmt.Errorf("%s: stream delivered no response", c.name)
		}
		// The caller's `stream` flag stays true across this internal retry, so
		// it will not rewrite the answer to the writer itself. Honour the
		// documented contract here instead: a streaming request delivers its
		// content to StreamWriter. Nothing was live-written (!received requires
		// zero content), so this writes the answer exactly once.
		w := req.StreamWriter
		req.Stream = false
		req.StreamWriter = nil
		out, err := c.ChatTurn(callCtx, req)
		if err != nil {
			return out, err
		}
		if w != nil && out != nil && out.Content != "" {
			if _, werr := io.WriteString(w, out.Content); werr != nil {
				return out, fmt.Errorf("%s: stream writer: %w", c.name, werr)
			}
		}
		return out, nil
	}
	return &Response{
		Content:          content,
		ReasoningContent: reasoning,
		WebSearch:        webSearch,
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		CacheUsage:       c.cacheUsage(usage),
		TokenUsage:       deriveTokenUsage(usage),
	}, nil
}

// readTurnStream parses SSE deltas into content + tool_calls.
// Content is written live to w until the first tool_calls delta. timeout is
// the armed per-request Timeout (0 = transport backstop) and is used only to
// name the deadline in read-error messages.
func (c *OpenAICompat) readTurnStream(ctx context.Context, body io.Reader, w io.Writer, timeout time.Duration) (string, string, []WebSearchResult, []ToolCall, string, bool, *usageWire, error) {
	var content strings.Builder
	var reasoning strings.Builder
	var webSearch []WebSearchResult
	toolsByIdx := map[int]*ToolCall{}
	finishReason := ""
	sawDone := false
	liveWrite := true
	var usage *usageWire

	sc := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", "", nil, nil, "", false, nil, fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, timeout, ctx.Err()), deadlineLabel(timeout))
			}
			return "", "", nil, nil, "", false, nil, ctx.Err()
		default:
		}
		line := sc.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			break
		}
		if err := c.applyStreamChunk(data, &content, &reasoning, &webSearch, toolsByIdx, &finishReason, &liveWrite, &usage, w); err != nil {
			return "", "", nil, nil, "", false, nil, err
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", "", nil, nil, "", false, nil, asTransient(fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, timeout, err), deadlineLabel(timeout)))
		}
		// A stream torn mid-body never delivered an answer.
		return "", "", nil, nil, "", false, nil, asTransient(fmt.Errorf("%s: stream read: %w", c.name, err))
	}
	payload := content.Len() > 0 || reasoning.Len() > 0 || len(webSearch) > 0 || len(toolsByIdx) > 0
	// An empty answer can be the real answer (a stop with no text, a turn whose
	// output was filtered). Treating the absence of payload as "nothing arrived"
	// re-sends the whole prompt non-streamed, so one turn is billed twice - and
	// the retry carries a fresh Idempotency-Key, so no upstream can dedupe it.
	// A finish_reason is the upstream saying the turn completed, so it counts as
	// received even with nothing to show. So does a captured usage object: the
	// upstream answered with accounting for the turn, so re-sending it would
	// bill the same turn twice. A bare [DONE] with no chunk at all is not -
	// that is an empty stream and the fallback still earns its keep.
	received := payload || finishReason != "" || usage != nil
	// A clean EOF is not a completion signal: bufio.Scanner returns nil from
	// Err() whether the server finished or a proxy cut the connection. Without
	// [DONE] or a finish_reason, whatever arrived is a fragment - returning it
	// as a successful turn means half an answer is presented as final, or a
	// tool runs on truncated argument JSON.
	//
	// A stream with tool_calls but no finish_reason may have incomplete
	// argument JSON. Text-only streams without a finish signal are usable
	// as-is: the content was fully received via streaming deltas.
	if len(toolsByIdx) > 0 && !sawDone && finishReason == "" {
		// Tool calls without a finish signal may be incomplete.
		// Check that all tool calls have an ID and name (minimum viable).
		for _, tc := range toolsByIdx {
			if tc.ID == "" || tc.Function.Name == "" {
				return "", "", nil, nil, "", false, nil, &TransientError{Err: fmt.Errorf("%s: stream ended without a completion signal (truncated tool call)", c.name)}
			}
			args := strings.TrimSpace(tc.Function.Arguments)
			if args != "" && !json.Valid([]byte(args)) {
				return "", "", nil, nil, "", false, nil, fmt.Errorf("%s: stream ended without a completion signal (tool call %q has malformed arguments)", c.name, tc.ID)
			}
		}
		// All tool calls have minimum structure; treat as complete.
	}
	return content.String(), reasoning.String(), webSearch, orderedToolCalls(toolsByIdx), finishReason, received, usage, nil
}

func (c *OpenAICompat) applyStreamChunk(
	data string,
	content *strings.Builder,
	reasoning *strings.Builder,
	webSearch *[]WebSearchResult,
	toolsByIdx map[int]*ToolCall,
	finishReason *string,
	liveWrite *bool,
	usage **usageWire,
	w io.Writer,
) error {
	if c.errorParser != nil {
		parserBody := []byte(data)
		if len(parserBody) > 4096 {
			parserBody = parserBody[:4096]
		}
		if err := c.errorParser(http.StatusOK, parserBody); err != nil {
			return err
		}
	}
	var chunk chatResponseBody
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil
	}
	// A trailing usage-only chunk commonly carries an empty choices array
	// (see TestOpenAIErrorParserPassesCleanCompletions's "empty choices with
	// usage" case) - capture it before the choices-length guard below would
	// otherwise discard it along with the rest of that chunk.
	if chunk.Usage != nil {
		*usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	ch := chunk.Choices[0]
	if ch.FinishReason != "" {
		*finishReason = ch.FinishReason
	}
	if len(ch.Delta.ToolCalls) > 0 {
		*liveWrite = false
	}
	if delta := ch.Delta.Content; delta != "" {
		content.WriteString(delta)
		if *liveWrite && w != nil {
			if _, err := io.WriteString(w, delta); err != nil {
				return err
			}
		}
	}
	if delta := ch.Delta.ReasoningContent; delta != "" {
		reasoning.WriteString(delta)
	}
	*webSearch = append(*webSearch, ch.Delta.WebSearch...)
	mergeToolCallDeltas(toolsByIdx, ch.Delta.ToolCalls)
	return nil
}

func mergeToolCallDeltas(toolsByIdx map[int]*ToolCall, deltas []streamToolCallDelta) {
	for _, tc := range deltas {
		idx, ok := deltaSlot(toolsByIdx, tc)
		acc, exists := toolsByIdx[idx]
		if !exists {
			acc = &ToolCall{Type: "function"}
			toolsByIdx[idx] = acc
		}
		_ = ok
		if tc.ID != "" {
			acc.ID = tc.ID
		}
		if tc.Type != "" {
			acc.Type = tc.Type
		}
		if tc.Function.Name != "" {
			acc.Function.Name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			acc.Function.Arguments += tc.Function.Arguments
		}
	}
}

// deltaSlot picks the accumulator a fragment belongs to. With an explicit
// index the provider owns the numbering. Without one, a fragment carrying a new
// ID starts a new call and a fragment carrying no ID continues the most recent
// one - otherwise every call collapses into slot 0.
func deltaSlot(toolsByIdx map[int]*ToolCall, tc streamToolCallDelta) (int, bool) {
	if tc.Index != nil {
		return *tc.Index, true
	}
	next := -1
	for i, acc := range toolsByIdx {
		if i > next {
			next = i
		}
		if tc.ID != "" && acc.ID == tc.ID {
			return i, true
		}
	}
	if tc.ID == "" && next >= 0 {
		return next, true // continuation of the newest call
	}
	return next + 1, false
}

func orderedToolCalls(toolsByIdx map[int]*ToolCall) []ToolCall {
	maxIdx := -1
	for i := range toolsByIdx {
		if i > maxIdx {
			maxIdx = i
		}
	}
	var out []ToolCall
	for i := 0; i <= maxIdx; i++ {
		if acc, ok := toolsByIdx[i]; ok {
			if acc.Type == "" {
				acc.Type = "function"
			}
			out = append(out, *acc)
		}
	}
	return out
}
