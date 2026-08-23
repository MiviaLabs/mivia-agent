package sdkadapter

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// TestTokenUsageToSDKUsageReportedRoundTrip is the bridge test for the
// positive case: a CLI-side TokenUsage carries Reported=true along with
// populated input and output counts. The SDK Usage shape always reports a
// TotalTokens field that the CLI does not, so the SDK-side reconstruction
// adds prompt + completion.
func TestTokenUsageToSDKUsageReportedRoundTrip(t *testing.T) {
	cli := provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}
	got := TokenUsageToSDKUsage(cli)
	want := sdkshape.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	if got != want {
		t.Fatalf("TokenUsageToSDKUsage(%+v) = %+v, want %+v", cli, got, want)
	}
	back, err := SDKUsageToTokenUsage(got)
	if err != nil {
		t.Fatalf("SDKUsageToTokenUsage round-trip error: %v", err)
	}
	if back != cli {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", back, cli)
	}
}

// TestTokenUsageToSDKUsageUnreportedYieldsZero is the bridge test for the
// CLI Reported=false case. The SDK side has no "not reported" concept; a
// zero value is the canonical mapping, and the reverse direction returns
// an unreported TokenUsage.
func TestTokenUsageToSDKUsageUnreportedYieldsZero(t *testing.T) {
	cli := provider.TokenUsage{}
	got := TokenUsageToSDKUsage(cli)
	if got != (sdkshape.Usage{}) {
		t.Fatalf("Unreported CLI usage must map to SDK zero usage, got %+v", got)
	}
	back, err := SDKUsageToTokenUsage(got)
	if err != nil {
		t.Fatalf("SDKUsageToTokenUsage round-trip error: %v", err)
	}
	if back != cli {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", back, cli)
	}
}

// TestSDKUsageToTokenUsageCachedTokensRejected pins the asymmetric shape:
// the SDK side has CachedTokens, the CLI side has no equivalent. A bridge
// in the SDK -> CLI direction that silently dropped CachedTokens would
// under-report cache reuse; the SDK adapter refuses the conversion
// instead so the caller can decide what to do.
func TestSDKUsageToTokenUsageCachedTokensRejected(t *testing.T) {
	u := sdkshape.Usage{PromptTokens: 100, CompletionTokens: 50, CachedTokens: 25}
	_, err := SDKUsageToTokenUsage(u)
	if err == nil {
		t.Fatalf("SDKUsageToTokenUsage with CachedTokens>0 must return an error, got nil")
	}
}

// TestSDKUsageToTokenUsageTotalDropped confirms that the SDK TotalTokens
// field is not preserved on the way back: the CLI does not store it, and
// adding the count would invent a reported observation.
func TestSDKUsageToTokenUsageTotalDropped(t *testing.T) {
	u := sdkshape.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 999}
	got, err := SDKUsageToTokenUsage(u)
	if err != nil {
		t.Fatalf("SDKUsageToTokenUsage: %v", err)
	}
	want := provider.TokenUsage{Reported: true, InputTokens: 100, OutputTokens: 50}
	if got != want {
		t.Fatalf("SDKUsageToTokenUsage dropped TotalTokens: got %+v, want %+v", got, want)
	}
}

// TestChatStreamProxyWritesToWriter confirms that the ChatStream proxy
// forwards the underlying completer and writes the streamed content into
// the caller-supplied writer.
func TestChatStreamProxyWritesToWriter(t *testing.T) {
	var wrote bytes.Buffer
	stub := &fakeCompleter{streamed: "hello world"}
	got, err := ChatStream(context.Background(), stub, sdkshape.Request{}, &wrote)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("ChatStream returned %q, want %q", got, "hello world")
	}
	if wrote.String() != "hello world" {
		t.Fatalf("ChatStream proxy wrote %q, want %q", wrote.String(), "hello world")
	}
}

// fakeCompleter satisfies the bits of provider.Completer that ChatStream
// needs without pulling in a network client. Only ChatStream is wired.
type fakeCompleter struct {
	streamed string
}

func (f *fakeCompleter) Name() string { return "fake" }
func (f *fakeCompleter) Chat(ctx context.Context, _ provider.Request) (string, error) {
	return f.streamed, nil
}
func (f *fakeCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: f.streamed}, nil
}
func (f *fakeCompleter) ChatStream(ctx context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != io.Discard && w != nil {
		_, _ = io.WriteString(w, f.streamed)
	}
	return f.streamed, nil
}
