package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func silentTurnRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
}

// TestNoMessageLossEmptyTurnFailsLoudly locks the defect where a model response
// with no content and no tool calls ended the turn as a *successful* completion:
// nothing appended to history, no event emitted, nothing written to the writer,
// and a nil error. The interactive surface reported "done" having produced
// nothing, which read as the agent silently stopping mid-task.
func TestNoMessageLossEmptyTurnFailsLoudly(t *testing.T) {
	cases := []struct {
		name  string
		steps []provider.Response
	}{
		{
			name:  "empty on the very first step",
			steps: []provider.Response{{Content: "", FinishReason: "stop"}},
		},
		{
			name: "empty after silent tool work",
			steps: []provider.Response{
				{Content: "", FinishReason: "tool_calls",
					ToolCalls: []provider.ToolCall{tc("1", "read_file", `{"path":"x"}`)}},
				{Content: "", FinishReason: "length"},
			},
		},
		{
			name:  "whitespace-only final response",
			steps: []provider.Response{{Content: "\n  \n", FinishReason: "stop"}},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			// retryOnEmptyResponse (agentloop_run.go) replays the whole turn
			// from scratch up to maxEmptyResponseRetries times when it stays
			// empty, so the scripted step sequence must repeat that many
			// times too - otherwise scriptCompleter falls back to its own
			// "done" default partway through and this test would no longer
			// pin the "no text ANYWHERE" contract it exists to lock.
			steps := make([]provider.Response, 0, len(tt.steps)*(1+maxEmptyResponseRetries))
			for i := 0; i < 1+maxEmptyResponseRetries; i++ {
				steps = append(steps, tt.steps...)
			}
			loop := &Loop{Completer: &scriptCompleter{steps: steps}, Tools: silentTurnRegistry(t)}
			_, err := loop.Run(context.Background(), "go", Options{Model: "m", MaxSteps: 10, RequireFinalText: true})
			if err == nil {
				t.Fatal("a turn that produced no text anywhere must not report success")
			}
			if !strings.Contains(err.Error(), "no assistant text") {
				t.Fatalf("error should name the cause, got: %v", err)
			}
		})
	}
}

// TestNoMessageLossEmptyTurnStaysSuccessfulForSubAgents is the counterweight to
// the test above. Sub-agents run the same Loop, and buildResult deletes the
// task output whenever the error is non-nil - so a sub-agent that did its work
// through tools and then stopped without prose would be reported as a failed
// task with its output discarded. Only callers that opt in get the error.
func TestNoMessageLossEmptyTurnStaysSuccessfulForSubAgents(t *testing.T) {
	steps := []provider.Response{
		{Content: "", FinishReason: "tool_calls",
			ToolCalls: []provider.ToolCall{tc("1", "read_file", `{"path":"x"}`)}},
		{Content: "", FinishReason: "stop"},
	}
	loop := &Loop{Completer: &scriptCompleter{steps: steps}, Tools: silentTurnRegistry(t)}
	if _, err := loop.Run(context.Background(), "go", Options{Model: "m", MaxSteps: 10}); err != nil {
		t.Fatalf("without RequireFinalText an empty turn must stay successful, got: %v", err)
	}
}

// TestNoMessageLossPreservesLeadingWhitespace guards the emptiness check from
// becoming a data change. TrimSpace is the right *predicate* for "did the model
// say anything", but storing the trimmed text strips the indentation off an
// answer that opens with a code block, so it stops rendering as one.
func TestNoMessageLossPreservesLeadingWhitespace(t *testing.T) {
	const answer = "    func main() {\n        println(\"hi\")\n    }\n"
	var sink strings.Builder
	loop := &Loop{
		Completer: &scriptCompleter{steps: []provider.Response{{Content: answer, FinishReason: "stop"}}},
		Tools:     silentTurnRegistry(t),
	}
	text, err := loop.Run(context.Background(), "show me", Options{
		Model: "m", MaxSteps: 10, RequireFinalText: true, FinalWriter: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != answer {
		t.Fatalf("returned text was altered:\n got %q\nwant %q", text, answer)
	}
	if sink.String() != answer {
		t.Fatalf("streamed text was altered:\n got %q\nwant %q", sink.String(), answer)
	}
	last := loop.Messages[len(loop.Messages)-1]
	if last.Role != provider.RoleAssistant || last.Content != answer {
		t.Fatalf("persisted message was altered:\n got %q\nwant %q", last.Content, answer)
	}
}

// cancelMidStreamCompleter streams a partial answer and then reports the turn
// was interrupted, the shape a Ctrl+C or a request deadline produces.
type cancelMidStreamCompleter struct {
	partial string
	err     error
}

func (cancelMidStreamCompleter) Name() string { return "cancel-mid-stream" }
func (c cancelMidStreamCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "", c.err
}
func (c cancelMidStreamCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "", c.err
}
func (c cancelMidStreamCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		_, _ = io.WriteString(req.StreamWriter, c.partial)
	}
	return nil, c.err
}

// TestNoMessageLossInterruptedTurnKeepsWhatWasShown locks the defect where an
// interrupted turn's assistant text was dropped from history. The bytes had
// already been streamed to the user's screen, but runStep discarded the response
// on error, so the transcript - and the next request - lost text the user read.
func TestNoMessageLossInterruptedTurnKeepsWhatWasShown(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"cancelled", context.Canceled},
		{"deadline exceeded", context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const partial = "Both fixes work. Here is the pro"
			var sink strings.Builder
			loop := &Loop{
				Completer: cancelMidStreamCompleter{partial: partial, err: tt.err},
				Tools:     silentTurnRegistry(t),
			}
			_, err := loop.Run(context.Background(), "prove it", Options{Model: "m", MaxSteps: 5, FinalWriter: &sink})
			if err == nil {
				t.Fatal("interrupted turn must still report the error")
			}
			if sink.String() != partial {
				t.Fatalf("partial never reached the writer: %q", sink.String())
			}
			last := loop.Messages[len(loop.Messages)-1]
			if last.Role != provider.RoleAssistant || last.Content != partial {
				t.Fatalf("interrupted turn lost the text the user saw; last message: role=%q content=%q",
					last.Role, last.Content)
			}
		})
	}
}

// TestCanceledContextBeforeCallFailsFastWithoutInvokingProvider asserts
// the SDK path's fail-fast behavior for a context already canceled
// BEFORE Run starts: it never invokes the completer at all (no wasted
// provider call on a doomed context), reports ctx.Canceled, and history
// keeps only the user message - there is no partial to preserve because
// nothing ever streamed. This intentionally differs from the legacy
// loop, which called the provider regardless and could surface the
// provider's own transport error instead of the generic cancellation;
// the SDK's early exit is a deliberate improvement (see
// docs/development/sdk-backend-field-mapping.md), not a gap to close.
func TestCanceledContextBeforeCallFailsFastWithoutInvokingProvider(t *testing.T) {
	const partial = "answer already shown"
	var sink strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loop := &Loop{
		Completer: cancelMidStreamCompleter{partial: partial, err: errors.New("stream closed by transport")},
		Tools:     silentTurnRegistry(t),
	}
	_, err := loop.Run(ctx, "queued follow-up", Options{Model: "m", MaxSteps: 5, FinalWriter: &sink})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if sink.String() != "" {
		t.Fatalf("provider must never be invoked on an already-canceled context, but streamed %q", sink.String())
	}
	if len(loop.Messages) != 1 || loop.Messages[0].Role != provider.RoleUser {
		t.Fatalf("history = %+v, want only the user message (no provider call happened)", loop.Messages)
	}
}

// TestNoMessageLossTruncatedTurnDoesNotPoisonHistory is the counterweight. A
// stream that ended without a completion signal is a fragment, not a turn:
// admitting it to history replays half an answer to the API as though it were
// complete. Only interruption keeps its partial.
func TestNoMessageLossTruncatedTurnDoesNotPoisonHistory(t *testing.T) {
	var sink strings.Builder
	loop := &Loop{
		Completer: cancelMidStreamCompleter{
			partial: "half an ans",
			err:     errors.New("provider: stream ended without a completion signal (truncated response)"),
		},
		Tools: silentTurnRegistry(t),
	}
	if _, err := loop.Run(context.Background(), "go", Options{
		Model: "m", MaxSteps: 5, FinalWriter: &sink,
	}); err == nil {
		t.Fatal("expected the truncation error")
	}
	for _, msg := range loop.Messages {
		if msg.Role == provider.RoleAssistant {
			t.Fatalf("a truncated fragment must not enter history: %q", msg.Content)
		}
	}
}
