package provider

import (
	"context"
	"io"
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
