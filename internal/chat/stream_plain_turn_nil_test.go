package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// nilRespCompleter's ChatTurn returns a nil *provider.Response and a nil
// error - a shape no real provider produces, but the one streamPlainTurn's
// own nil-guard exists for.
type nilRespCompleter struct{}

func (nilRespCompleter) Name() string { return "nil-resp" }
func (nilRespCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (nilRespCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (nilRespCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, nil
}

// TestStreamPlainTurn_NilResponseIsEmpty pins streamPlainTurn's own
// resp == nil guard.
func TestStreamPlainTurn_NilResponseIsEmpty(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, nilRespCompleter{})
	reply, reasoning, err := sess.streamPlainTurn(context.Background(), provider.Request{}, io.Discard, nilRespCompleter{})
	if err != nil {
		t.Fatalf("streamPlainTurn: %v", err)
	}
	if reply != "" || reasoning != "" {
		t.Fatalf("reply=%q reasoning=%q, want both empty for a nil response", reply, reasoning)
	}
}
