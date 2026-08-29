package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// resolveReasoningContent picks the reasoning text to carry onto the
// assistant message: the plain reasoning_content field, then the legacy
// reasoning field, then a concatenation of structured reasoning_details.
func resolveReasoningContent(reasoningContent, reasoning string, details []reasoningDetailWire) string {
	if reasoningContent != "" {
		return reasoningContent
	}
	if reasoning != "" {
		return reasoning
	}
	var b strings.Builder
	for _, d := range details {
		if d.Text != "" {
			b.WriteString(d.Text)
		} else if d.Summary != "" {
			b.WriteString(d.Summary)
		}
	}
	return b.String()
}

// retryTurnWithoutStreaming re-asks a whole turn non-streamed and returns the
// complete response, tool calls and accounting included. It is the recovery
// path for a tool-capable turn whose stream delivered nothing.
//
// The re-ask has to be the SAME QUESTION. nonStreamRequest copies the request
// and clears only the streaming fields, so tools, tool_choice, reasoning, and
// the replay flag all survive; the earlier hand-built literal silently omitted
// req.Tools, which asked a model that had been offered tools to answer with
// none of them.
func (c *OpenAICompat) retryTurnWithoutStreaming(ctx context.Context, req Request, w io.Writer) (*Response, error) {
	resp, err := c.ChatTurn(ctx, c.nonStreamRequest(req))
	if err != nil {
		return nil, err
	}
	// The fallback fires only when nothing was live-written, so writing here
	// delivers the answer exactly once. Nil-safe: callers may pass no writer.
	if w != nil && resp.Content != "" {
		if _, werr := io.WriteString(w, resp.Content); werr != nil {
			return nil, werr
		}
	}
	return resp, nil
}

// retryWithoutStreaming is the text-only form of the same recovery, for
// ChatStream, whose contract is the answer text alone. It shares
// retryTurnWithoutStreaming's request handling so the two cannot drift.
func (c *OpenAICompat) retryWithoutStreaming(ctx context.Context, req Request, w io.Writer) (string, error) {
	resp, err := c.retryTurnWithoutStreaming(ctx, req, nil)
	if err != nil {
		return "", err
	}
	content := resp.Content
	// The fallback fires only when nothing was live-written, so writing the
	// answer here delivers it exactly once. Mirrors the tool-capable path in
	// chatTurnStream. Nil-safe: ChatStream allows a nil writer.
	if w != nil {
		if _, werr := io.WriteString(w, content); werr != nil {
			return content, werr
		}
	}
	return content, nil
}

// handleStreamError parses an in-band provider error envelope from a stream
// chunk and returns the (content, error) the read loop should surface, plus
// whether a non-streamed re-ask was performed. A transient fault that
// delivered nothing is re-asked once non-streamed; otherwise the accumulated
// content and the error are returned unchanged.
func (c *OpenAICompat) handleStreamError(ctx context.Context, req Request, w io.Writer, data, full string, delivered bool) (string, error, bool) {
	if c.errorParser == nil {
		return full, nil, false
	}
	parserBody := []byte(data)
	if len(parserBody) > 4096 {
		parserBody = parserBody[:4096]
	}
	if err := c.errorParser(http.StatusOK, parserBody); err != nil {
		if IsTransient(err) && !delivered && !req.DisableProviderReplay {
			out, rerr := c.retryWithoutStreaming(ctx, req, w)
			return out, rerr, true
		}
		return full, err, false
	}
	return full, nil, false
}
