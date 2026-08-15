package provider

import (
	"context"
	"io"
	"testing"
)

// reasoningTerminalHistory builds a message history with two resolved
// assistant tool-call rounds, each carrying a large ReasoningContent: an
// OLD round (fully superseded by a later assistant turn) and a TERMINAL
// round (the newest tool exchange, nothing after it but its own results).
// Only the terminal round is "the current round" a
// ReasoningBillingTerminalExchange provider would actually replay on the
// wire.
func reasoningTerminalHistory(oldReasoning, terminalReasoning string) []Message {
	return []Message{
		{Role: RoleSystem, Content: "system prompt"},
		{Role: RoleUser, Content: "do the task"},
		{
			Role:             RoleAssistant,
			ReasoningContent: oldReasoning,
			ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: "{}"}}},
		},
		{Role: RoleTool, ToolCallID: "call-1", Content: "old tool result"},
		{
			Role:             RoleAssistant,
			ReasoningContent: terminalReasoning,
			ToolCalls: []ToolCall{{ID: "call-2", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read_file", Arguments: "{}"}}},
		},
		{Role: RoleTool, ToolCallID: "call-2", Content: "terminal tool result"},
	}
}

// TestTerminalExchangeProfileSkipsHistoricalReasoning proves
// ReasoningBillingTerminalExchange charges the terminal round's
// ReasoningContent but not an already-resolved earlier round's, while
// ReasoningBillingAllTurns (the zero-value default) charges both.
func TestTerminalExchangeProfileSkipsHistoricalReasoning(t *testing.T) {
	oldReasoning := stringOfLen(4000)
	terminalReasoning := "short"
	msgs := reasoningTerminalHistory(oldReasoning, terminalReasoning)

	allTurns := MessagesTokens(msgs, ContextAccountingProfile{ReasoningBilling: ReasoningBillingAllTurns})
	terminalOnly := MessagesTokens(msgs, ContextAccountingProfile{ReasoningBilling: ReasoningBillingTerminalExchange})

	if terminalOnly >= allTurns {
		t.Fatalf("terminal-exchange estimate (%d) must be strictly cheaper than all-turns (%d): the old round's 4000-byte reasoning must not be billed", terminalOnly, allTurns)
	}
	// The gap must be attributable to the old round's reasoning (roughly
	// len(oldReasoning)/4 tokens), not some unrelated drift.
	gap := allTurns - terminalOnly
	wantGap := estimateTokens(oldReasoning)
	if gap != wantGap {
		t.Fatalf("gap between profiles = %d, want exactly the old round's reasoning cost %d", gap, wantGap)
	}
}

// TestReasoningBillingNeverChargesNothing proves the "never" mode charges
// neither round's ReasoningContent.
func TestReasoningBillingNeverChargesNothing(t *testing.T) {
	msgs := reasoningTerminalHistory(stringOfLen(2000), stringOfLen(2000))
	withReasoning := MessagesTokens(msgs, ContextAccountingProfile{ReasoningBilling: ReasoningBillingAllTurns})
	never := MessagesTokens(msgs, ContextAccountingProfile{ReasoningBilling: ReasoningBillingNever})
	if never >= withReasoning {
		t.Fatalf("ReasoningBillingNever estimate (%d) must be cheaper than all-turns (%d)", never, withReasoning)
	}
}

// TestUnknownProviderDefaultsToConservativeBilling proves the zero-value
// ContextAccountingProfile (what an unrecognized or generic provider gets
// through ContextAccountingFor's type-assertion fallback) bills identically
// to an explicit ReasoningBillingAllTurns profile - the conservative
// "bill everything" default the task requires for any provider that
// declares nothing.
func TestUnknownProviderDefaultsToConservativeBilling(t *testing.T) {
	msgs := reasoningTerminalHistory(stringOfLen(3000), stringOfLen(500))
	zeroValue := MessagesTokens(msgs, ContextAccountingProfile{})
	explicitAllTurns := MessagesTokens(msgs, ContextAccountingProfile{ReasoningBilling: ReasoningBillingAllTurns})
	if zeroValue != explicitAllTurns {
		t.Fatalf("zero-value profile estimate (%d) must equal explicit all-turns (%d)", zeroValue, explicitAllTurns)
	}

	// ContextAccountingFor on a Completer that does not implement
	// ContextAccountingAware (every hand-written test fake, and any future
	// unrecognized provider) must resolve to that same conservative default.
	var unaware Completer = unawareCompleter{}
	if got := ContextAccountingFor(unaware); got != (ContextAccountingProfile{}) {
		t.Fatalf("ContextAccountingFor(unaware completer) = %+v, want zero value", got)
	}
}

// TestSharedAccountingPin proves the planner-style estimator
// (EstimateMessagesPromptCost, fed a pre-hoisted schema cost) and the
// request-style estimator (EstimateRequestCost/EstimatePromptCost, called
// directly with the tool list) price the SAME messages/tools/profile to the
// exact same token count. internal/contextmgr's planner uses the former;
// the agent loop's calibration estimate (loop_request.go) uses the latter -
// a mismatch between the two would mean calibration corrects against a
// different notion of "cost" than the trigger that used the calibration.
func TestSharedAccountingPin(t *testing.T) {
	msgs := reasoningTerminalHistory(stringOfLen(1500), stringOfLen(1500))
	tools := []ToolSpec{
		{"type": "function", "function": map[string]any{"name": "read_file", "description": "reads a file"}},
	}
	for _, profile := range []ContextAccountingProfile{
		{ReasoningBilling: ReasoningBillingAllTurns},
		{ReasoningBilling: ReasoningBillingTerminalExchange},
		{ReasoningBilling: ReasoningBillingNever},
	} {
		schemaCost, err := EstimateToolSchemaCost(tools)
		if err != nil {
			t.Fatal(err)
		}
		plannerStyle := EstimateMessagesPromptCost(msgs, schemaCost, profile)
		requestStyle, err := EstimateRequestCost(msgs, tools, 0, profile)
		if err != nil {
			t.Fatal(err)
		}
		promptStyle, err := EstimatePromptCost(msgs, tools, profile)
		if err != nil {
			t.Fatal(err)
		}
		if plannerStyle != requestStyle {
			t.Fatalf("profile %+v: planner-style estimate %d != request-style %d", profile, plannerStyle, requestStyle)
		}
		if requestStyle != promptStyle {
			t.Fatalf("profile %+v: request-style estimate %d != EstimatePromptCost %d", profile, requestStyle, promptStyle)
		}
	}
}

func stringOfLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// unawareCompleter implements Completer but not ContextAccountingAware,
// modeling every existing test fake and any provider this package has never
// heard of.
type unawareCompleter struct{}

func (unawareCompleter) Name() string { return "unaware" }
func (unawareCompleter) ChatStream(ctx context.Context, req Request, w io.Writer) (string, error) {
	return "", nil
}
func (unawareCompleter) Chat(ctx context.Context, req Request) (string, error) { return "", nil }
func (unawareCompleter) ChatTurn(ctx context.Context, req Request) (*Response, error) {
	return nil, nil
}
