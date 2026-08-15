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

// retryWithoutStreaming falls back to a non-streaming Chat call, used when a
// stream attempt fails before any content is delivered.
func (c *OpenAICompat) retryWithoutStreaming(ctx context.Context, req Request, w io.Writer) (string, error) {
	content, err := c.Chat(ctx, Request{
		Model:            req.Model,
		Messages:         req.Messages,
		Temperature:      req.Temperature,
		MaxTokens:        req.MaxTokens,
		ToolChoice:       req.ToolChoice,
		Timeout:          req.Timeout,
		Stream:           false,
		ReasoningLevel:   req.ReasoningLevel,
		ReasoningDialect: req.ReasoningDialect,
		SessionID:        req.SessionID,
	})
	if err != nil {
		return content, err
	}
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
