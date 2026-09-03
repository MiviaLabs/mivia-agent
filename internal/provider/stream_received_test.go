package provider

import (
	"encoding/json"
	"testing"
)

// readTurnReceivedShared mirrors readTurnStream's received flag on the
// dimensions both stream paths share: content, the reasoning builder
// (reasoning_content, reasoning, and payload-bearing reasoning_details),
// top-level body.WebSearch, finish_reason, and usage. tool_calls and
// delta-level web_search are excluded: readTurnStream tracks those in its
// payload while the no-tools readStream cannot receive them, so disagreement
// on those dimensions is an expected scope difference, not a bug.
func reasoningDeltaCarriesPayload(reasoningContent, reasoning string, details []reasoningDetailWire) bool {
	if reasoningContent != "" || reasoning != "" {
		return true
	}
	for _, d := range details {
		if d.Text != "" || d.Summary != "" {
			return true
		}
	}
	return false
}

func readTurnReceivedShared(body chatResponseBody) bool {
	finishReason := ""
	contentLen, reasoningLen, webSearchLen := 0, 0, 0
	if len(body.Choices) > 0 {
		ch := body.Choices[0]
		finishReason = ch.FinishReason
		if ch.Delta.Content != "" {
			contentLen = 1 // content.Len() > 0 in readTurnStream
		}
		if reasoningDeltaCarriesPayload(ch.Delta.ReasoningContent, ch.Delta.Reasoning, ch.Delta.ReasoningDetails) {
			reasoningLen = 1 // reasoning.Len() > 0 in readTurnStream
		}
		if len(ch.Delta.WebSearch) > 0 {
			webSearchLen = 1 // delta-level append into the same accumulator
		}
	}
	if len(body.WebSearch) > 0 {
		webSearchLen = 1 // top-level append
	}
	// payload := content || reasoning || webSearch (tools excluded above)
	return contentLen > 0 || reasoningLen > 0 || webSearchLen > 0 ||
		finishReason != "" || body.Usage != nil
}

// readStreamReceivedShared mirrors readStream's received flag on the shared
// dimensions. It models readStream's gate by calling the production predicate
// deltaCountsAsReceived (plus the top-level body.WebSearch term), so a
// divergence between the two paths' received rules is reported here instead
// of hidden by a hand-rewritten copy: reasoning and reasoning_details are
// received dimensions on both paths, and an empty reasoning_details entry
// must not count on either (R0-1). An empty-choices chunk still counts as
// received when it carries usage or top-level web_search, matching readStream.
func readStreamReceivedShared(body chatResponseBody) bool {
	if len(body.Choices) == 0 {
		return body.Usage != nil || len(body.WebSearch) > 0
	}
	ch := body.Choices[0]
	return deltaCountsAsReceived(ch.Delta.Content, ch.Delta.ReasoningContent, ch.Delta.Reasoning, ch.FinishReason, ch.Delta.ReasoningDetails, body.Usage) ||
		len(body.WebSearch) > 0
}

// FuzzReadStreamReceived compares the received-flag behaviour of readStream
// against readTurnStream for identical single-chunk JSON inputs on their
// shared dimensions (see readTurnReceivedShared and readStreamReceivedShared).
// The reasoning shape is shared in full: readStream counts reasoning_content,
// reasoning, and a reasoning_details entry carrying text or summary exactly
// like readTurnStream's reasoning.Len()>0 payload term, and an entry with
// neither counts on NO path (R0-1). TOP-LEVEL body.WebSearch is shared too:
// both paths count it as a received signal, so the motivating sole-signal
// shape {"choices":[{"delta":{}}],"web_search":[{"title":"x"}]} must agree on
// received=true. Chunks whose delta carries tool_calls or delta-level
// web_search are skipped, since readStream cannot receive those dimensions.
func FuzzReadStreamReceived(f *testing.F) {
	seeds := []string{
		`{"choices":[{"delta":{}}]}`,
		`{"choices":[{"delta":{"content":""}}]}`,
		`{"choices":[{"delta":{"content":"hello"}}]}`,
		`{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{},"finish_reason":""}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1}}`,
		`{"choices":[],"web_search":[{"title":"x"}]}`,
		`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1}}`,
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"think"},"finish_reason":"stop"}]}`,
		`{"choices":[{"delta":{"reasoning":"think"}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think"}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"s"}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}`,
		`{"choices":[{"delta":{}}],"web_search":[{"title":"x"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		// Must be valid JSON.
		var body chatResponseBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Skip()
		}
		// Empty stream (bare [DONE]) — no chunk to evaluate. An empty-choices
		// chunk carrying usage or top-level web_search IS a chunk: both paths
		// count those as received, so it must be evaluated, not skipped.
		if len(body.Choices) == 0 && body.Usage == nil && len(body.WebSearch) == 0 {
			t.Skip()
		}
		// Skip chunks whose delta carries dimensions readTurnStream tracks but
		// readStream does not (tool_calls, delta-level web_search). TOP-LEVEL
		// body.WebSearch is NOT skipped: both paths count it as received.
		hasToolCalls := len(body.Choices) > 0 && len(body.Choices[0].Delta.ToolCalls) > 0
		hasDeltaWebSearch := len(body.Choices) > 0 && len(body.Choices[0].Delta.WebSearch) > 0
		if hasToolCalls || hasDeltaWebSearch {
			t.Skip()
		}
		if readStreamReceivedShared(body) != readTurnReceivedShared(body) {
			t.Errorf("received mismatch for %q: readStream=%v readTurnStream=%v",
				string(raw), readStreamReceivedShared(body), readTurnReceivedShared(body))
		}
	})
}
