package adapter

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestFallbackCompleter_ReportsNoActiveClient covers fallbackCompleter's
// Name and its three stub methods (Chat, ChatStream, ChatTurn): the
// completer sessionBindingFactory installs when a loading session's saved
// provider/model can't be built at all (no provider.New succeeded either).
// Every dispatch attempt against it must fail closed with an error naming
// the provider it stands in for, never a silent empty success.
func TestFallbackCompleter_ReportsNoActiveClient(t *testing.T) {
	const providerName = "stale-provider"
	c := fallbackCompleter{providerName: providerName}

	if got := c.Name(); got != providerName {
		t.Errorf("Name() = %q, want %q", got, providerName)
	}

	wantSubstrings := []string{providerName, "no active client"}

	if _, err := c.Chat(context.Background(), provider.Request{}); err == nil {
		t.Error("Chat: want non-nil error, got nil")
	} else {
		for _, want := range wantSubstrings {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Chat error = %q, want it to contain %q", err.Error(), want)
			}
		}
	}

	if _, err := c.ChatStream(context.Background(), provider.Request{}, io.Discard); err == nil {
		t.Error("ChatStream: want non-nil error, got nil")
	} else {
		for _, want := range wantSubstrings {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ChatStream error = %q, want it to contain %q", err.Error(), want)
			}
		}
	}

	if _, err := c.ChatTurn(context.Background(), provider.Request{}); err == nil {
		t.Error("ChatTurn: want non-nil error, got nil")
	} else {
		for _, want := range wantSubstrings {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ChatTurn error = %q, want it to contain %q", err.Error(), want)
			}
		}
	}
}
