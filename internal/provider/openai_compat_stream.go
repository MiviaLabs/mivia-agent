package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// deltaCountsAsReceived reports whether a stream chunk counts as a delivered
// answer (content, any reasoning shape, finish_reason, or usage), mirroring
// readTurnStream's payload gate so the no-tools path never re-bills a
// reasoning-only stream non-streamed.
func deltaCountsAsReceived(content, reasoningContent, reasoning, finishReason string, details []reasoningDetailWire, usage *usageWire) bool {
	if content != "" || reasoningContent != "" || reasoning != "" || finishReason != "" || usage != nil {
		return true
	}
	// A reasoning_details entry counts only when it carries text or summary.
	// captureReasoningDetails folds exactly those entries into the reasoning
	// builder that gates readTurnStream's received flag, so an entry with
	// neither must not suppress the non-streaming fallback here either (R0-1):
	// the two stream paths must agree on the same wire shape.
	for _, d := range details {
		if d.Text != "" || d.Summary != "" {
			return true
		}
	}
	return false
}

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
	resp, req, err := c.doChatRequest(callCtx, req)
	if err != nil {
		return nil, asTransient(err)
	}
	defer resp.Body.Close()

	content, reasoning, webSearch, toolCalls, finishReason, received, usage, err := c.readTurnStream(callCtx, c.wrapWithIdleWatchdog(resp.Body), req.StreamWriter, req.Timeout)
	if err != nil {
		// A transient 200-in-band provider error delivered nothing. Re-ask the
		// turn once non-streamed instead of surfacing it as a terminal failure,
		// unless replay is disabled or content already reached the writer. The
		// re-ask stays bounded to a single attempt.
		if IsTransient(err) && !received && !req.DisableProviderReplay {
			if errors.Is(err, ErrStreamIdle) {
				// Visible-on-recovery: previously this fallback fired silently,
				// so a stalled stream that self-healed via non-streaming retry
				// left no trace an operator could see.
				log.Printf("%s: stream stalled (idle timeout, bound %s); recovering via non-streaming retry", c.name, streamIdleTimeout())
			}
			// The whole response, not just its text: this turn offered tools,
			// and a stall must not silently downgrade it to prose with no
			// tool calls, no finish reason, and no token accounting.
			retried, rerr := c.retryTurnWithoutStreaming(callCtx, req, req.StreamWriter)
			if rerr != nil {
				return nil, rerr
			}
			return retried, nil
		}
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

	// The cancel check runs before the scan so a line the scanner accepted
	// still reaches the writer if the context fires while it is processed.
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", "", nil, nil, "", false, nil, fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, timeout, ctx.Err()), deadlineLabel(timeout))
			}
			return "", "", nil, nil, "", false, nil, ctx.Err()
		default:
		}
		if !sc.Scan() {
			break
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
			return turnStreamErrorReturn(&content, &reasoning, webSearch, toolsByIdx, finishReason, usage, err)
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
		// Tool calls without a finish signal may be incomplete; require the
		// minimum viable structure (ID + name, valid argument JSON).
		if err := validateTruncatedToolCalls(toolsByIdx, c.name); err != nil {
			return turnStreamErrorReturn(&content, &reasoning, webSearch, toolsByIdx, finishReason, usage, err)
		}
		// All tool calls have minimum structure; treat as complete.
	}
	return content.String(), reasoning.String(), webSearch, orderedToolCalls(toolsByIdx), finishReason, received, usage, nil
}

// turnStreamErrorReturn assembles the tuple a read loop returns on failure,
// reporting whether anything actually reached the wire so the caller never
// re-asks a turn that already delivered an answer.
func turnStreamErrorReturn(content, reasoning *strings.Builder, webSearch []WebSearchResult, toolsByIdx map[int]*ToolCall, finishReason string, usage *usageWire, err error) (string, string, []WebSearchResult, []ToolCall, string, bool, *usageWire, error) {
	received := content.Len() > 0 || reasoning.Len() > 0 || len(webSearch) > 0 || len(toolsByIdx) > 0 || finishReason != "" || usage != nil
	return content.String(), reasoning.String(), webSearch, orderedToolCalls(toolsByIdx), finishReason, received, usage, err
}

// validateTruncatedToolCalls rejects tool-call fragments that ended without a
// completion signal and lack the minimum viable structure: every call needs an
// ID and a name, and arguments must be valid JSON when present.
func validateTruncatedToolCalls(toolsByIdx map[int]*ToolCall, name string) error {
	for _, tc := range toolsByIdx {
		if tc.ID == "" || tc.Function.Name == "" {
			return &TransientError{Err: fmt.Errorf("%s: stream ended without a completion signal (truncated tool call)", name)}
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args != "" && !json.Valid([]byte(args)) {
			return &TransientError{Err: fmt.Errorf("%s: stream ended without a completion signal (tool call %q has malformed arguments)", name, tc.ID)}
		}
	}
	return nil
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
	// A provider may attach search results at the chunk top level (the wire
	// struct decodes body.WebSearch, and the non-stream path honors it first).
	// Capture them before the choices-length guard below would otherwise drop
	// them along with an empty-choices chunk - mirroring the trailing-usage
	// capture above, so top-level web_search survives on both choices-bearing
	// and empty-choices chunks. len(webSearch)>0 already counts as payload in
	// readTurnStream, so a sole-signal web_search chunk is a completion signal
	// and never re-bills the turn non-streamed.
	if len(chunk.WebSearch) > 0 {
		*webSearch = append(*webSearch, chunk.WebSearch...)
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
	if delta := ch.Delta.Reasoning; delta != "" {
		reasoning.WriteString(delta)
	}
	captureReasoningDetails(reasoning, ch.Delta.ReasoningContent, ch.Delta.ReasoningDetails)
	*webSearch = append(*webSearch, ch.Delta.WebSearch...)
	mergeToolCallDeltas(toolsByIdx, ch.Delta.ToolCalls)
	return nil
}

// captureReasoningDetails folds reasoning_details entries into the reasoning
// builder when the chunk carries no canonical reasoning_content. Every
// payload-bearing entry contributes, whatever its type tag: the reasoning
// builder doubles as the completion signal in readTurnStream (reasoning.Len()
// > 0 gates re-billing), and the non-stream resolveReasoningContent
// concatenates every entry's text (or summary when text is absent) with no
// type gate. An entry with neither text nor summary contributes nothing, so
// an empty details array still does not count as received (R0-1).
func captureReasoningDetails(reasoning *strings.Builder, deltaContent string, details []reasoningDetailWire) {
	if deltaContent != "" || len(details) == 0 {
		return
	}
	for _, d := range details {
		if d.Text != "" {
			reasoning.WriteString(d.Text)
		} else if d.Summary != "" {
			reasoning.WriteString(d.Summary)
		}
	}
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

// readStream reads one SSE body on the no-tools ChatStream path and returns
// the assembled content. It moved here from openai_compat.go to keep that
// file under its line budget; the behavior is unchanged.
func (c *OpenAICompat) readStream(ctx context.Context, req Request, body io.Reader, w io.Writer) (string, error) {
	var full strings.Builder
	received := false
	sc := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	// The cancel check runs before the scan so a line the scanner accepted
	// still reaches the writer if the context fires while it is processed.
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return full.String(), fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, req.Timeout, ctx.Err()), deadlineLabel(req.Timeout))
			}
			return full.String(), ctx.Err()
		default:
		}
		if !sc.Scan() {
			break
		}
		line := sc.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if out, err, replayed := c.handleStreamError(ctx, req, w, data, full.String(), full.Len() > 0 || received); replayed || err != nil {
			return out, err
		}
		var chunk chatResponseBody
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			// A usage-only chunk, or one carrying top-level web_search, is a
			// completion signal. Do not replay the turn.
			if chunk.Usage != nil || len(chunk.WebSearch) > 0 {
				received = true
			}
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		streamDelta := chunk.Choices[0].Delta
		// Any delivered wire shape (content/reasoning/finish/usage) must not
		// re-bill. Top-level web_search is also a delivered shape: readStream
		// cannot surface the entries (it returns content only), but the signal
		// must count as received so a web_search-only turn is not re-asked
		// non-streamed (mirrors readTurnStream's len(webSearch)>0 payload gate).
		if deltaCountsAsReceived(delta, streamDelta.ReasoningContent, streamDelta.Reasoning, chunk.Choices[0].FinishReason, streamDelta.ReasoningDetails, chunk.Usage) || len(chunk.WebSearch) > 0 {
			received = true
		}
		if delta == "" {
			continue
		}
		full.WriteString(delta)
		if w != nil {
			if _, err := io.WriteString(w, delta); err != nil {
				return full.String(), err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return full.String(), fmt.Errorf("%s: stream read: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, req.Timeout, err), deadlineLabel(req.Timeout))
		}
		return full.String(), fmt.Errorf("%s: stream read: %w", c.name, err)
	}
	if full.Len() == 0 && !received {
		if req.DisableProviderReplay {
			return "", fmt.Errorf("%s: stream delivered no response", c.name)
		}
		return c.retryWithoutStreaming(ctx, req, w)
	}
	return full.String(), nil
}
