package chat

import (
	"context"
	"io"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type fakeCompleter struct {
	err error
	out string
}

func (f *fakeCompleter) Name() string { return "fake" }

func (f *fakeCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return f.ChatStream(ctx, req, io.Discard)
}

func (f *fakeCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if w != nil {
		_, _ = io.WriteString(w, f.out)
	}
	return f.out, nil
}

func (f *fakeCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &provider.Response{Content: f.out, FinishReason: "stop"}, nil
}

func TestClearAndUserTurns(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{out: "ok"})
	if len(s.Messages) != 1 {
		t.Fatalf("want system message")
	}
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.UserTurns() != 1 {
		t.Fatalf("turns=%d", s.UserTurns())
	}
	s.Clear()
	if s.UserTurns() != 0 || len(s.Messages) != 1 {
		t.Fatalf("clear failed: turns=%d msgs=%d", s.UserTurns(), len(s.Messages))
	}
}

func TestFailedSendDropsUserTurn(t *testing.T) {
	res := &config.Resolved{Model: "m", SystemPrompt: "sys"}
	s := NewSession(res, &fakeCompleter{err: context.Canceled})
	_, err := s.SendUser(context.Background(), "hi", io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	if s.UserTurns() != 0 {
		t.Fatalf("user turn should be dropped, turns=%d", s.UserTurns())
	}
}
