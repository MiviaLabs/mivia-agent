package chatsync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestPrivacyGatesDefaultOff(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{}) // IncludeToolIO: false, IncludeThinking: false

	// 1. Tool start: input is omitted, named in redacted
	weStart := p.Project(events.Event{
		Kind:       events.KindToolStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "write_file",
		Input:      `{"path":"/secret.txt","content":"classified"}`,
		Timestamp:  time.Now(),
	})
	if len(weStart) != 1 {
		t.Fatalf("tool start produced %d events, want 1", len(weStart))
	}
	pStart := weStart[0].Payload.(*ToolStartedPayload)
	if pStart.Input != "" {
		t.Errorf("input = %q, want '' when IncludeToolIO is false", pStart.Input)
	}
	if pStart.InputBytes == 0 {
		t.Error("input_bytes is 0, want real byte length")
	}
	if len(pStart.Redacted) != 1 || pStart.Redacted[0] != "input" {
		t.Errorf("redacted = %v, want ['input']", pStart.Redacted)
	}

	// Verify JSON output omits "input" key
	dataStart, _ := json.Marshal(pStart)
	var mStart map[string]any
	_ = json.Unmarshal(dataStart, &mStart)
	if _, present := mStart["input"]; present {
		t.Errorf("json carries 'input' key when withheld: %s", string(dataStart))
	}

	// 2. Tool end: output is omitted, named in redacted
	weEnd := p.Project(events.Event{
		Kind:       events.KindToolEnd,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "write_file",
		Output:     "wrote 42 bytes",
		Timestamp:  time.Now(),
	})
	if len(weEnd) != 1 {
		t.Fatalf("tool end produced %d events, want 1", len(weEnd))
	}
	pEnd := weEnd[0].Payload.(*ToolEndedPayload)
	if pEnd.Output != "" {
		t.Errorf("output = %q, want '' when IncludeToolIO is false", pEnd.Output)
	}
	if len(pEnd.Redacted) != 1 || pEnd.Redacted[0] != "output" {
		t.Errorf("redacted = %v, want ['output']", pEnd.Redacted)
	}

	// 3. Thinking: text is omitted
	weThink := p.Project(events.Event{
		Kind:      events.KindThinking,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "secret thoughts",
		Timestamp: time.Now(),
	})
	if len(weThink) != 1 {
		t.Fatalf("thinking produced %d events, want 1", len(weThink))
	}
	pThink := weThink[0].Payload.(*ThinkingDeltaPayload)
	if pThink.Text != "" {
		t.Errorf("thinking text = %q, want '' when IncludeThinking is false", pThink.Text)
	}
	if pThink.Bytes != len("secret thoughts") {
		t.Errorf("thinking bytes = %d, want %d", pThink.Bytes, len("secret thoughts"))
	}
}

func TestPrivacyGatesToolIOEnabledWithRedactionPolicy(t *testing.T) {
	// Install a real redact.Policy
	pol, err := redact.Compile([]string{`SECRET_KEY_[0-9]+`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	oldPol := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(oldPol) })

	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeToolIO: true, IncludeThinking: true})

	weStart := p.Project(events.Event{
		Kind:       events.KindToolStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "api_call",
		Input:      `{"token":"SECRET_KEY_998877"}`,
		Timestamp:  time.Now(),
	})
	if len(weStart) != 1 {
		t.Fatalf("tool start produced %d events, want 1", len(weStart))
	}
	pStart := weStart[0].Payload.(*ToolStartedPayload)
	if strings.Contains(pStart.Input, "SECRET_KEY_998877") {
		t.Errorf("input %q contains unredacted secret", pStart.Input)
	}
	if !strings.Contains(pStart.Input, "[redacted]") {
		t.Errorf("input %q does not contain placeholder", pStart.Input)
	}
}

func TestPrivacyGatesToolIORespectsRedactToolArgs(t *testing.T) {
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	// Even with IncludeToolIO: true, tools.RedactToolArgs() forces tool IO off (AND composition)
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeToolIO: true})

	weStart := p.Project(events.Event{
		Kind:       events.KindToolStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "cmd",
		Input:      "ls -la",
		Timestamp:  time.Now(),
	})
	if len(weStart) != 1 {
		t.Fatalf("tool start produced %d events, want 1", len(weStart))
	}
	pStart := weStart[0].Payload.(*ToolStartedPayload)
	if pStart.Input != "" {
		t.Errorf("input = %q, want '' because tools.RedactToolArgs() is active", pStart.Input)
	}
	if len(pStart.Redacted) == 0 || pStart.Redacted[0] != "input" {
		t.Errorf("redacted = %v, want ['input']", pStart.Redacted)
	}
}

func TestFieldTruncationByteBudgets(t *testing.T) {
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(false) })

	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeToolIO: true})

	// Output of 20 KiB (> 16 KiB budget)
	largeOutput := strings.Repeat("A", 20*1024)
	weEnd := p.Project(events.Event{
		Kind:       events.KindToolEnd,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "cat",
		Output:     largeOutput,
		Timestamp:  time.Now(),
	})
	if len(weEnd) != 1 {
		t.Fatalf("tool end produced %d events, want 1", len(weEnd))
	}
	pEnd := weEnd[0].Payload.(*ToolEndedPayload)
	if len(pEnd.Output) != BudgetToolOutput {
		t.Errorf("output len = %d, want %d", len(pEnd.Output), BudgetToolOutput)
	}
	if pEnd.Trunc == nil || pEnd.Trunc.Fields["output"].Kept != BudgetToolOutput || pEnd.Trunc.Fields["output"].Total != 20*1024 {
		t.Errorf("trunc = %+v, want output kept=%d, total=%d", pEnd.Trunc, BudgetToolOutput, 20*1024)
	}
}

func TestRuneSafeTruncation(t *testing.T) {
	// 3-byte UTF-8 rune: '世' (E4 B8 96)
	// 4 copies = 12 bytes. Truncating at 5 bytes should keep 3 bytes (1 rune) rather than 5 invalid bytes.
	str := "世界世界"
	kept, keptLen, totalLen, truncated := truncateString(str, 5)
	if !truncated {
		t.Error("truncated is false, want true")
	}
	if totalLen != 12 {
		t.Errorf("totalLen = %d, want 12", totalLen)
	}
	if kept != "世" || keptLen != 3 {
		t.Errorf("kept = %q (%d bytes), want '世' (3 bytes)", kept, keptLen)
	}
}
