package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// setTestReasoningRedactionPolicy installs a minimal policy matching
// secret-<digits> and restores whatever policy was active before, so a test
// never leaves a process-wide policy installed for its neighbors. The policy
// is a process-wide global: no test in this package may call t.Parallel while
// depending on it.
func setTestReasoningRedactionPolicy(t *testing.T, patterns ...string) {
	t.Helper()
	old := redact.Current()
	policy, err := redact.Compile(patterns, nil, "[redacted]")
	if err != nil {
		t.Fatalf("compile redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(old) })
}

// TestEmitReasoningRedactsEventThinking pins that chain-of-thought surfaced
// through EventThinking passes through the configured redaction policy before
// reaching OnEvent consumers: an operator-facing sink must never see a raw
// secret the workspace's policy knows about.
func TestEmitReasoningRedactsEventThinking(t *testing.T) {
	setTestReasoningRedactionPolicy(t, `(?i)secret-[0-9]+`)

	var got *Event
	opts := Options{OnEvent: func(e Event) {
		if e.Kind == EventThinking {
			c := e
			got = &c
		}
	}}
	emitReasoning(opts, &provider.Response{ReasoningContent: "deliberation secret-1234 done"})
	if got == nil {
		t.Fatal("no EventThinking emitted")
	}
	if !strings.Contains(got.Content, "[redacted]") {
		t.Fatalf("event thinking not redacted: %q", got.Content)
	}
	if strings.Contains(got.Content, "secret-1234") {
		t.Fatalf("event thinking leaked secret: %q", got.Content)
	}
}

// TestEmitReasoningKeepsHistoryRaw separates the two jobs emitReasoning's
// caller performs: the event sink gets a redacted copy, but the reasoning
// committed to host history (l.Messages) stays verbatim so the model's next
// request can replay it. Redaction must never rewrite what the provider said.
func TestEmitReasoningKeepsHistoryRaw(t *testing.T) {
	setTestReasoningRedactionPolicy(t, `(?i)secret-[0-9]+`)

	comp := &recordingCompleter{
		steps: []provider.Response{
			{Content: "final answer", FinishReason: "stop", ReasoningContent: "deliberation secret-1234 done"},
		},
	}
	loop := &Loop{Completer: comp, Tools: tools.NewRegistry()}
	var got *Event
	if _, err := loop.Run(context.Background(), "say hi", Options{Model: "m",
		MaxSteps: 3,
		OnEvent: func(e Event) {
			if e.Kind == EventThinking {
				c := e
				got = &c
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no EventThinking emitted")
	}
	if !strings.Contains(got.Content, "[redacted]") {
		t.Fatalf("event thinking not redacted: %q", got.Content)
	}
	if strings.Contains(got.Content, "secret-1234") {
		t.Fatalf("event thinking leaked secret: %q", got.Content)
	}

	const raw = "deliberation secret-1234 done"
	var stored string
	for _, m := range loop.Messages {
		if m.Role == provider.RoleAssistant && m.Content == "final answer" {
			stored = m.ReasoningContent
		}
	}
	if stored != raw {
		t.Fatalf("history reasoning was altered: got %q want %q", stored, raw)
	}
}

// TestEmitReasoningNilPolicyIdentity documents the fail-open posture for
// reasoning: with no policy installed (the default), emitReasoning passes
// content through unchanged. Redaction is a configured workspace property,
// never a compiled-in list.
func TestEmitReasoningNilPolicyIdentity(t *testing.T) {
	old := redact.Current()
	redact.SetPolicy(nil)
	defer redact.SetPolicy(old)

	const raw = "deliberation secret-1234 done"
	var got *Event
	opts := Options{OnEvent: func(e Event) {
		if e.Kind == EventThinking {
			c := e
			got = &c
		}
	}}
	emitReasoning(opts, &provider.Response{ReasoningContent: raw})
	if got == nil {
		t.Fatal("no EventThinking emitted")
	}
	if got.Content != raw {
		t.Fatalf("nil-policy identity broken: got %q want %q", got.Content, raw)
	}
}
