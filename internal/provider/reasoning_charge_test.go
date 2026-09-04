package provider

import "testing"

// EstimateReasoningTokensAt exists so a caller can split a message's prose
// cost from its reasoning cost WITHOUT restating the billing rule. That is
// the whole contract: whatever EstimateMessageTokensAt charges for
// ReasoningContent at a position, this returns, and at a position that is
// not billed for it, this returns nothing.
//
// A second copy of the rule is exactly what it prevents - the sidebar's
// context breakdown subtracts this from the message charge, so a drift
// between the two would move tokens from "thinking" into "messages" with
// the total still adding up and nothing failing.

func reasoningPair() []Message {
	return []Message{
		{Role: RoleUser, Content: "prompt"},
		{Role: RoleAssistant, Content: "answer", ReasoningContent: "considering the retry policy shape"},
	}
}

// TestReasoningChargeMatchesWhatTheMessageChargeIncluded is the
// discriminator: strip the reasoning and the message must get cheaper by
// exactly the amount this function reported.
func TestReasoningChargeMatchesWhatTheMessageChargeIncluded(t *testing.T) {
	for _, profile := range []ContextAccountingProfile{
		{},
		{ReasoningBilling: ReasoningBillingTerminalExchange},
	} {
		msgs := reasoningPair()
		const idx = 1

		charged := EstimateReasoningTokensAt(msgs, idx, profile)
		withReasoning := EstimateMessageTokensAt(msgs, idx, profile)

		stripped := reasoningPair()
		stripped[idx].ReasoningContent = ""
		withoutReasoning := EstimateMessageTokensAt(stripped, idx, profile)

		if got := withReasoning - withoutReasoning; got != charged {
			t.Errorf("profile %+v: the message charge fell by %d when reasoning was removed, but EstimateReasoningTokensAt reported %d",
				profile, got, charged)
		}
	}
}

// TestAPositionNotBilledForReasoningIsChargedNothing: a message with no
// reasoning, and a profile that never bills for it, both charge nothing -
// so a caller subtracting this from the message cost cannot subtract more
// than the message cost.
//
// Out-of-range indices are deliberately not exercised: billsReasoningAt
// indexes msgs[index] directly and every caller derives the index from a
// range over the same slice, so an out-of-range read is a caller bug that
// panics rather than a case this function answers. Adding a bounds check
// here would be an arm nothing can reach.
func TestAPositionNotBilledForReasoningIsChargedNothing(t *testing.T) {
	msgs := reasoningPair()

	// A user message carries no reasoning at all.
	if got := EstimateReasoningTokensAt(msgs, 0, ContextAccountingProfile{ReasoningBilling: ReasoningBillingTerminalExchange}); got != 0 {
		t.Errorf("a user message was charged %d for reasoning", got)
	}
	// A profile that never bills charges nothing even where reasoning exists.
	if got := EstimateReasoningTokensAt(msgs, 1, ContextAccountingProfile{ReasoningBilling: ReasoningBillingNever}); got != 0 {
		t.Errorf("ReasoningBillingNever charged %d", got)
	}
	// And where it does bill, the charge is real - otherwise the two
	// assertions above would pass on a function that always returns 0.
	if got := EstimateReasoningTokensAt(msgs, 1, ContextAccountingProfile{}); got <= 0 {
		t.Errorf("a billed position was charged %d, want a positive charge", got)
	}
}
