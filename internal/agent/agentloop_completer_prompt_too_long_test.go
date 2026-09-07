package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// hostPromptTooLongCompleter rejects with the HOST sentinel exactly as the
// production wire layers do (internal/provider/openai_errors.go:76,103 and
// zai_errors.go:106 wrap provider.ErrPromptTooLong), which is the shape the
// SDK-facing seam actually receives in production.
type hostPromptTooLongCompleter struct {
	calls int
}

func (c *hostPromptTooLongCompleter) Name() string { return "host-prompt-too-long" }

func (c *hostPromptTooLongCompleter) Chat(context.Context, provider.Request) (string, error) {
	c.calls++
	return "", fmt.Errorf("openai: context length exceeded: %w", provider.ErrPromptTooLong)
}

func (c *hostPromptTooLongCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	_, err := c.Chat(ctx, req)
	return nil, err
}

func (c *hostPromptTooLongCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}

// The SDK's in-loop prompt-too-long recovery gates on
// errors.Is(err, sdkshape.ErrPromptTooLong) (mivia-ai-sdk/agentloop/run.go),
// a sentinel distinct from the host's own provider.ErrPromptTooLong. Host
// providers only ever wrap the host sentinel, so unless this seam translates,
// the SDK Window recovery can never fire for any real provider rejection -
// and on adopted turns the host retry is suppressed on that very premise
// (agentloop_recovery.go), leaving the turn with no recovery at all.
func TestCompleterTranslatesHostPromptTooLongToSDKSentinel(t *testing.T) {
	cli := &hostPromptTooLongCompleter{}
	a, err := newAgentLoopCompleter(cli)
	if err != nil {
		t.Fatal(err)
	}

	_, chatErr := a.Chat(context.Background(), sdkshape.Request{Model: "m"})
	if !errors.Is(chatErr, sdkshape.ErrPromptTooLong) {
		t.Fatalf("Chat err = %v, want errors.Is sdkshape.ErrPromptTooLong so the SDK Window recovery can fire", chatErr)
	}
	// The host sentinel must survive too: the non-adopted host retry
	// (runSDKPromptTooLongRecoverable) still keys on it.
	if !errors.Is(chatErr, provider.ErrPromptTooLong) {
		t.Fatalf("Chat err = %v, want the host sentinel preserved", chatErr)
	}

	ch, streamErr := a.ChatStream(context.Background(), sdkshape.Request{Model: "m"})
	if streamErr != nil {
		t.Fatalf("ChatStream returned err = %v, want the error on the chunk", streamErr)
	}
	var chunkErr error
	for c := range ch {
		if c.Err != nil {
			chunkErr = c.Err
		}
	}
	if !errors.Is(chunkErr, sdkshape.ErrPromptTooLong) {
		t.Fatalf("ChatStream chunk err = %v, want errors.Is sdkshape.ErrPromptTooLong", chunkErr)
	}
	if !errors.Is(chunkErr, provider.ErrPromptTooLong) {
		t.Fatalf("ChatStream chunk err = %v, want the host sentinel preserved", chunkErr)
	}
}

// An unrelated provider error must pass through untouched: translation is
// keyed on the sentinel, not applied to every failure.
func TestCompleterLeavesUnrelatedErrorsUntranslated(t *testing.T) {
	boom := errors.New("connection reset")
	cli := &fixedErrCompleter{err: boom}
	a, err := newAgentLoopCompleter(cli)
	if err != nil {
		t.Fatal(err)
	}
	_, chatErr := a.Chat(context.Background(), sdkshape.Request{Model: "m"})
	if !errors.Is(chatErr, boom) {
		t.Fatalf("Chat err = %v, want the original error", chatErr)
	}
	if errors.Is(chatErr, sdkshape.ErrPromptTooLong) {
		t.Fatalf("Chat err = %v, must not be tagged as prompt-too-long", chatErr)
	}
}

type fixedErrCompleter struct{ err error }

func (c *fixedErrCompleter) Name() string { return "fixed-err" }

func (c *fixedErrCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}

func (c *fixedErrCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, c.err
}

func (c *fixedErrCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", c.err
}

// nilTurnThenErrCompleter exercises Chat's defensive fallback: ChatTurn
// returns (nil, nil), so Chat falls through to the plain Chat call, which
// then fails. That fallback must translate the sentinel too - it is the same
// provider rejection arriving by a different route.
type nilTurnThenErrCompleter struct{ err error }

func (c *nilTurnThenErrCompleter) Name() string { return "nil-turn-then-err" }

func (c *nilTurnThenErrCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}

func (c *nilTurnThenErrCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, nil
}

func (c *nilTurnThenErrCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", c.err
}

func TestCompleterTranslatesOnChatFallbackPath(t *testing.T) {
	cli := &nilTurnThenErrCompleter{err: fmt.Errorf("zai: 1261: %w", provider.ErrPromptTooLong)}
	a, err := newAgentLoopCompleter(cli)
	if err != nil {
		t.Fatal(err)
	}
	_, chatErr := a.Chat(context.Background(), sdkshape.Request{Model: "m"})
	if !errors.Is(chatErr, sdkshape.ErrPromptTooLong) {
		t.Fatalf("Chat fallback err = %v, want errors.Is sdkshape.ErrPromptTooLong", chatErr)
	}
	if !errors.Is(chatErr, provider.ErrPromptTooLong) {
		t.Fatalf("Chat fallback err = %v, want the host sentinel preserved", chatErr)
	}
}
