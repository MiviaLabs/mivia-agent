package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
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

type processCancelCompleter struct{ cancel func() }

func (processCancelCompleter) Name() string { return "process-cancel" }
func (c processCancelCompleter) Chat(context.Context, provider.Request) (string, error) {
	c.cancel()
	return "", context.Canceled
}
func (c processCancelCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return c.Chat(context.Background(), provider.Request{})
}
func (c processCancelCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.cancel()
	return nil, context.Canceled
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
	err := sendLineMode(session, "a question", nil, false)
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
	err := sendLineMode(session, "a question", signals, false)
	if err != nil {
		t.Fatalf("cancelled turn returned an error: %v", err)
	}
	if got := stderr(); !strings.Contains(got, "(cancelled)") {
		t.Fatalf("cancelled turn was not reported: %q", got)
	}
}

type streamingLineCompleter struct {
	output string
	err    error
}

func (streamingLineCompleter) Name() string { return "streaming-line" }
func (c streamingLineCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}
func (c streamingLineCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil {
		_, _ = io.WriteString(w, c.output)
	}
	return c.output, c.err
}
func (c streamingLineCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.output)
	}
	if c.err != nil {
		return nil, c.err
	}
	return &provider.Response{Content: c.output, FinishReason: "stop"}, nil
}

type cancelingLineCompleter struct {
	partial string
	cancel  context.CancelFunc
}

func (cancelingLineCompleter) Name() string { return "canceling-line" }
func (c cancelingLineCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.ChatStream(ctx, req, io.Discard)
}
func (c cancelingLineCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	if w != nil {
		_, _ = io.WriteString(w, c.partial)
	}
	c.cancel()
	return "", context.Canceled
}
func (c cancelingLineCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	c.cancel()
	return nil, context.Canceled
}

func TestOneShotPrintsReplyBeforeAutosaveFailure(t *testing.T) {
	const answer = "The answer survives."
	root := filepath.Join(t.TempDir(), "sessions")
	store, err := chat.NewFileSessionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	sess := chat.NewSession(&config.Resolved{ProviderName: "test", Model: "model", SystemPrompt: "sys"}, streamingLineCompleter{output: answer})
	sess.SetSessionStore(store, chat.NewSaveManager(store, "model", "test"))
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("blocks session directories"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err = oneShotContext(context.Background(), sess, "question", false, &config.Resolved{ProviderName: "test", Model: "model"}, false)
	gotOut, _ := stdout(), stderr()
	if !errors.Is(err, chat.ErrPersistence) {
		t.Fatalf("error = %v, want ErrPersistence", err)
	}
	if !strings.Contains(gotOut, answer) {
		t.Fatalf("stdout = %q, want it to contain %q", gotOut, answer)
	}
}

func TestOneShotPrintsPartialBeforeCancellation(t *testing.T) {
	const partial = "The partial answer survives."
	ctx, cancel := context.WithCancel(context.Background())
	sess := chat.NewSession(&config.Resolved{ProviderName: "test", Model: "model", SystemPrompt: "sys"}, cancelingLineCompleter{partial: partial, cancel: cancel})
	sess.Tools = tools.NewRegistry()
	sess.UseTools = true
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := oneShotContext(ctx, sess, "question", true, &config.Resolved{ProviderName: "test", Model: "model"}, false)
	gotOut, gotErr := stdout(), stderr()
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if !strings.Contains(gotOut, partial) {
		t.Fatalf("stdout = %q, want it to contain %q", gotOut, partial)
	}
	if !strings.Contains(gotErr, "(cancelled)") {
		t.Fatalf("stderr = %q, want a cancellation notice", gotErr)
	}
}

func TestOneShotHidesOrdinaryFailedStream(t *testing.T) {
	want := errors.New("provider failed")
	sess := chat.NewSession(&config.Resolved{ProviderName: "test", Model: "model", SystemPrompt: "sys"}, streamingLineCompleter{output: "uncommitted partial", err: want})
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := oneShotContext(context.Background(), sess, "question", false, &config.Resolved{ProviderName: "test", Model: "model"}, false)
	gotOut, _ := stdout(), stderr()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if strings.Contains(gotOut, "uncommitted partial") {
		t.Fatalf("stdout exposed uncommitted output: %q", gotOut)
	}
}

func TestCancellationCanReplaceOnlyCancellationErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", want: true},
		{name: "cancelled", err: context.Canceled, want: true},
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "persistence", err: chat.ErrPersistence},
		{name: "provider", err: errors.New("provider failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cancellationCanReplaceTurnError(test.err); got != test.want {
				t.Fatalf("result = %t, want %t", got, test.want)
			}
		})
	}
}

func TestShouldReportChatCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !shouldReportChatCancellation(ctx, context.Canceled) {
		t.Fatal("canceled turn did not report session cancellation")
	}
	if shouldReportChatCancellation(context.Background(), context.Canceled) {
		t.Fatal("live turn reported session cancellation")
	}
}

func TestProcessLineChatReportsInterruptedTurn(t *testing.T) {
	previous := classicTurnContext
	var cancel context.CancelFunc
	classicTurnContext = func(context.Context) (context.Context, context.CancelFunc) {
		ctx, next := context.WithCancel(context.Background())
		cancel = next
		return ctx, next
	}
	t.Cleanup(func() { classicTurnContext = previous })

	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, processCancelCompleter{cancel: func() { cancel() }})
	session.UseTools = true
	session.Tools = tools.NewRegistry()
	out := newMockTerminal()
	term := &Terminal{out: out, width: 80, height: 24}
	renderer := NewChatRenderer(term, "model")
	err := processLineChat("question", session, &config.Resolved{}, false, term, renderer, NewInputBuffer(" > "), "model")
	if err != nil {
		t.Fatalf("interrupted turn error = %v", err)
	}
	if !strings.Contains(out.String(), "cancelled - still in session") {
		t.Fatalf("output = %q", out.String())
	}
}

// TestSendLineModeJSONEmitsChunksThenDone pins the happy-path --json wire
// contract: streamed content comes out as one or more "chunk" events, in
// order, followed by exactly one "done" event, and stdout carries nothing
// else (no raw text, no trailing bare newline).
func TestSendLineModeJSONEmitsChunksThenDone(t *testing.T) {
	const answer = "hello there"
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, streamingLineCompleter{output: answer})
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := sendLineMode(session, "hi", nil, true)
	got := stdout()
	_ = stderr()
	if err != nil {
		t.Fatalf("sendLineMode: %v", err)
	}
	lines := splitNonEmptyLines(got)
	if len(lines) < 2 {
		t.Fatalf("got %d NDJSON lines, want at least 2 (chunk + done): %q", len(lines), got)
	}
	var reconstructed strings.Builder
	for _, line := range lines[:len(lines)-1] {
		var ev ndjsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", line, err)
		}
		if ev.Type != "chunk" {
			t.Fatalf("line %q: type = %q, want %q", line, ev.Type, "chunk")
		}
		reconstructed.WriteString(ev.Text)
	}
	if reconstructed.String() != answer {
		t.Fatalf("reconstructed chunk text = %q, want %q", reconstructed.String(), answer)
	}
	var last ndjsonEvent
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last line %q is not valid JSON: %v", lines[len(lines)-1], err)
	}
	if last.Type != "done" {
		t.Fatalf("last line type = %q, want %q", last.Type, "done")
	}
}

// TestSendLineModeJSONReportsErrorWithoutLeakingProviderText pins DC-14: a
// failed turn must surface an {"type":"error",...} event on stdout, but its
// message must never be the raw provider/tool error text verbatim, since that
// text can carry request content.
func TestSendLineModeJSONReportsErrorWithoutLeakingProviderText(t *testing.T) {
	secret := errors.New("request contained sk-live-super-secret-token, rejected")
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, failingLineCompleter{err: secret})
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := sendLineMode(session, "a question", nil, true)
	got := stdout()
	_ = stderr()
	if err == nil {
		t.Fatal("sendLineMode swallowed the turn error")
	}
	lines := splitNonEmptyLines(got)
	if len(lines) != 1 {
		t.Fatalf("got %d NDJSON lines, want exactly 1 (error): %q", len(lines), got)
	}
	var ev ndjsonEvent
	if jsonErr := json.Unmarshal([]byte(lines[0]), &ev); jsonErr != nil {
		t.Fatalf("line %q is not valid JSON: %v", lines[0], jsonErr)
	}
	if ev.Type != "error" {
		t.Fatalf("type = %q, want %q", ev.Type, "error")
	}
	if strings.Contains(ev.Message, "sk-live-super-secret-token") {
		t.Fatalf("error message leaked provider error text verbatim: %q", ev.Message)
	}
	if ev.Message == "" {
		t.Fatal("error event carried no message")
	}
}

// TestSendLineModeJSONReportsCancellationAsDistinctType pins the cancellation
// framing: a SIGINT'd turn is reported as {"type":"cancelled"} - not "done",
// and not folded into "error" - so a consumer can tell "the user stopped
// this" apart from "this failed" with a bare type check, no message-string
// matching. sendLineMode itself still returns nil (a user-initiated cancel
// is not a hard failure - see TestLineModeStillReportsCancellation for the
// non-json equivalent).
func TestSendLineModeJSONReportsCancellationAsDistinctType(t *testing.T) {
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, blockingLineCompleter{})
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := sendLineMode(session, "a question", signals, true)
	got := stdout()
	_ = stderr()
	if err != nil {
		t.Fatalf("cancelled turn returned an error: %v", err)
	}
	lines := splitNonEmptyLines(got)
	if len(lines) != 1 {
		t.Fatalf("got %d NDJSON lines, want exactly 1 (cancelled): %q", len(lines), got)
	}
	var ev ndjsonEvent
	if jsonErr := json.Unmarshal([]byte(lines[0]), &ev); jsonErr != nil {
		t.Fatalf("line %q is not valid JSON: %v", lines[0], jsonErr)
	}
	if ev.Type != "cancelled" {
		t.Fatalf("event = %+v, want type=cancelled", ev)
	}
	if ev.Message != "" {
		t.Fatalf("event = %+v, want no message on a cancelled event", ev)
	}
}

// TestSendLineModeNonJSONUnaffected pins that omitting --json leaves the
// classic line-mode output (raw streamed text plus a bare trailing newline)
// byte-for-byte as it was before --json existed.
func TestSendLineModeNonJSONUnaffected(t *testing.T) {
	const answer = "plain text reply"
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, streamingLineCompleter{output: answer})
	stdout := captureStdout(t)
	stderr := captureStderr(t)
	err := sendLineMode(session, "hi", nil, false)
	got := stdout()
	_ = stderr()
	if err != nil {
		t.Fatalf("sendLineMode: %v", err)
	}
	if got != answer+"\n" {
		t.Fatalf("stdout = %q, want %q", got, answer+"\n")
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
