package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type failingLineCompleter struct{ err error }

func (failingLineCompleter) Name() string { return "failing-line" }
func (c failingLineCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", c.err
}
func (c failingLineCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", c.err
}
func (c failingLineCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return nil, c.err
}

// TestLineModeReportsTurnFailures pins that the classic line-mode REPL returns
// a turn's error instead of swallowing it. It asked ctx.Err() after calling its
// own cancel(), so every turn printed "(cancelled)" and returned nil - which is
// why a durable publication failure was invisible on this surface and read as
// the user pressing Ctrl+C.
func TestLineModeReportsTurnFailures(t *testing.T) {
	want := errors.New("durable publication refused")
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, failingLineCompleter{err: want})
	stderr := captureStderr(t)
	err := sendLineMode(session, "a question", nil)
	if err == nil {
		t.Fatal("line mode swallowed the turn error")
	}
	if !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("line mode error = %v, want it to carry %v", err, want)
	}
	if got := stderr(); strings.Contains(got, "(cancelled)") {
		t.Fatalf("a failed turn was reported as cancelled: %q", got)
	}
}

// blockingLineCompleter waits for the interrupt to reach the context, which is
// what a real provider call does when the user presses Ctrl+C mid-turn.
type blockingLineCompleter struct{}

func (blockingLineCompleter) Name() string { return "blocking-line" }
func (c blockingLineCompleter) Chat(ctx context.Context, r provider.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (c blockingLineCompleter) ChatStream(ctx context.Context, r provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, r)
}
func (c blockingLineCompleter) ChatTurn(ctx context.Context, r provider.Request) (*provider.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestLineModeStillReportsCancellation keeps the cancelled path honest: a turn
// the user really did interrupt must still say so and must not surface an error.
func TestLineModeStillReportsCancellation(t *testing.T) {
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, blockingLineCompleter{})
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	stderr := captureStderr(t)
	err := sendLineMode(session, "a question", signals)
	if err != nil {
		t.Fatalf("cancelled turn returned an error: %v", err)
	}
	if got := stderr(); !strings.Contains(got, "(cancelled)") {
		t.Fatalf("cancelled turn was not reported: %q", got)
	}
}

// captureStderr redirects os.Stderr for the duration of a test and returns a
// reader for what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = write
	captured := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		captured <- string(data)
	}()
	var once bool
	var result string
	return func() string {
		if !once {
			once = true
			os.Stderr = original
			_ = write.Close()
			result = <-captured
			_ = read.Close()
		}
		return result
	}
}
