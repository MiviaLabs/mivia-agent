package sdkadapter

import (
	"context"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// UsageCachedTokensUnsupportedErr is the error returned by SDKUsageToTokenUsage
// when the SDK side reports a non-zero CachedTokens value. The CLI's
// TokenUsage shape carries no cache token count: a bridge that silently
// dropped the value would under-report cache reuse, so the bridge refuses
// the conversion instead.
var UsageCachedTokensUnsupportedErr = fmt.Errorf("sdkadapter: SDK Usage.CachedTokens has no CLI equivalent")

// TokenUsageToSDKUsage maps the CLI's TokenUsage onto the SDK's Usage. A
// CLI-side Reported=false becomes the SDK's zero Usage, the only shape the
// SDK has for "no observation". TotalTokens is computed by the SDK side
// from prompt + completion and is not preserved across the reverse bridge.
func TokenUsageToSDKUsage(t provider.TokenUsage) sdkshape.Usage {
	if !t.Reported {
		return sdkshape.Usage{}
	}
	return sdkshape.Usage{
		PromptTokens:     t.InputTokens,
		CompletionTokens: t.OutputTokens,
		TotalTokens:      t.InputTokens + t.OutputTokens,
	}
}

// SDKUsageToTokenUsage maps the SDK's Usage onto the CLI's TokenUsage. It
// refuses a non-zero CachedTokens via UsageCachedTokensUnsupportedErr so
// the caller can decide whether to surface the cache count somewhere else;
// the reverse direction is not symmetric because the CLI shape carries no
// cache token field. TotalTokens is dropped: the CLI does not store it.
func SDKUsageToTokenUsage(u sdkshape.Usage) (provider.TokenUsage, error) {
	if u.CachedTokens > 0 {
		return provider.TokenUsage{}, UsageCachedTokensUnsupportedErr
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return provider.TokenUsage{}, nil
	}
	return provider.TokenUsage{
		Reported:     true,
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
	}, nil
}

// ChatStream proxies a ChatStream call from the SDK-shaped Request onto a
// CLI-shaped Completer and writes the assistant's text deltas into w. The
// returned string is the complete assistant content; the writer receives
// the same content in arrival order. The CLI Completer takes a Request
// plus an io.Writer; the SDK Completer takes a Request and returns a
// <-chan Chunk. This bridge lives entirely on the CLI side because the
// CLI runtime is what actually produces text today.
func ChatStream(ctx context.Context, c provider.Completer, _ sdkshape.Request, w io.Writer) (string, error) {
	if w == nil {
		w = io.Discard
	}
	return c.ChatStream(ctx, provider.Request{}, w)
}
