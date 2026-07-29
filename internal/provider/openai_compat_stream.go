package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		return nil, fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.httpError(resp)
	}

	content, reasoning, webSearch, toolCalls, finishReason, received, err := c.readTurnStream(callCtx, resp.Body, req.StreamWriter)
	if err != nil {
		return nil, err
	}
	// Empty stream with no tools → fall back to non-stream once.
	if !received {
		req.Stream = false
		req.StreamWriter = nil
		return c.ChatTurn(ctx, req)
	}
	return &Response{
		Content:          content,
		ReasoningContent: reasoning,
		WebSearch:        webSearch,
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
	}, nil
}

// readTurnStream parses SSE deltas into content + tool_calls.
// Content is written live to w until the first tool_calls delta.
func (c *OpenAICompat) readTurnStream(ctx context.Context, body io.Reader, w io.Writer) (string, string, []WebSearchResult, []ToolCall, string, bool, error) {
	var content strings.Builder
	var reasoning strings.Builder
	var webSearch []WebSearchResult
	toolsByIdx := map[int]*ToolCall{}
	finishReason := ""
	liveWrite := true

	sc := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)

	for sc.Scan() {
		select {
		case <-ctx.Done():
			return "", "", nil, nil, "", false, ctx.Err()
		default:
		}
		line := sc.Text()
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if err := c.applyStreamChunk(data, &content, &reasoning, &webSearch, toolsByIdx, &finishReason, &liveWrite, w); err != nil {
			return "", "", nil, nil, "", false, err
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", nil, nil, "", false, fmt.Errorf("%s: stream read: %w", c.name, err)
	}
	received := content.Len() > 0 || reasoning.Len() > 0 || len(webSearch) > 0 || len(toolsByIdx) > 0
	return content.String(), reasoning.String(), webSearch, orderedToolCalls(toolsByIdx), finishReason, received, nil
}

func (c *OpenAICompat) applyStreamChunk(
	data string,
	content *strings.Builder,
	reasoning *strings.Builder,
	webSearch *[]WebSearchResult,
	toolsByIdx map[int]*ToolCall,
	finishReason *string,
	liveWrite *bool,
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
	if chunk.Error != nil && chunk.Error.Message != "" {
		return fmt.Errorf("%s: %s", c.name, sanitizeErr(chunk.Error.Message))
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
		acc, ok := toolsByIdx[tc.Index]
		if !ok {
			acc = &ToolCall{Type: "function"}
			toolsByIdx[tc.Index] = acc
		}
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
