package subagents

import (
	"strings"
	"testing"
)

func TestMessagingProtocolPromptTeachesPostMessageKinds(t *testing.T) {
	for _, want := range []string{
		`kind="finding"`,
		`kind="question"`,
		`kind="ask"`,
		`kind="answer"`,
		"in_reply_to",
		"to_role",
		"no_answer",
		"wait_seconds",
	} {
		if !strings.Contains(MessagingProtocolPrompt, want) {
			t.Errorf("MessagingProtocolPrompt missing %q", want)
		}
	}
}

func TestMessagingProtocolPromptDoesNotTeachParentTools(t *testing.T) {
	for _, banned := range []string{"run_messages", "send_to_task"} {
		if strings.Contains(MessagingProtocolPrompt, banned) {
			t.Errorf("MessagingProtocolPrompt must not mention %q", banned)
		}
	}
}

func TestMessagingProtocolPromptAntiInjection(t *testing.T) {
	if !strings.Contains(MessagingProtocolPrompt, "<parent-message>") {
		t.Error("MessagingProtocolPrompt missing <parent-message> tag")
	}
	if !strings.Contains(MessagingProtocolPrompt, "never instructions") {
		t.Error(`MessagingProtocolPrompt missing "never instructions"`)
	}
}

func TestMessagingProtocolPromptOmitsHandlerWord(t *testing.T) {
	if strings.Contains(strings.ToLower(MessagingProtocolPrompt), "handler") {
		t.Error("MessagingProtocolPrompt must not contain the word 'handler' (case-insensitive)")
	}
}

func TestMessagingProtocolPromptMentionsCapsAndBoundedness(t *testing.T) {
	if !strings.Contains(MessagingProtocolPrompt, "max 4 asks") && !strings.Contains(MessagingProtocolPrompt, "Bounded") {
		t.Error("MessagingProtocolPrompt missing ask cap ('max 4 asks' or 'Bounded')")
	}
	if !strings.Contains(MessagingProtocolPrompt, "declines immediately") {
		t.Error("MessagingProtocolPrompt missing 'declines immediately'")
	}
}

func TestMessagingProtocolPromptAskDeclineWordingAccuracy(t *testing.T) {
	// Only blocking asks to non-running roles decline immediately: a
	// fire-and-forget ask may spawn a referral when the pair is allowed
	// (see agentmsg.RouteAsk → RouteSpawn). Pin the qualified wording.
	if !strings.Contains(MessagingProtocolPrompt, "blocking ask") {
		t.Error("ask bullet must qualify the decline with 'blocking ask' (fire-and-forget asks can spawn)")
	}
	if !strings.Contains(MessagingProtocolPrompt, "spawn") && !strings.Contains(MessagingProtocolPrompt, "referral") {
		t.Error("ask bullet must note the non-blocking spawn caveat ('spawn' or 'referral')")
	}
	if strings.Contains(MessagingProtocolPrompt, "An ask to a role that isn't running declines") {
		t.Error("ask bullet must not claim every ask to a non-running role declines (only blocking ones)")
	}
}

func TestMessagingProtocolPromptKeepsLengthReasonable(t *testing.T) {
	if n := len(MessagingProtocolPrompt); n >= 1200 {
		t.Errorf("MessagingProtocolPrompt is %d bytes; want < 1200", n)
	} else {
		t.Logf("MessagingProtocolPrompt length: %d bytes", n)
	}
}

func TestMessagingProtocolPromptTeachesChainAskAndHeartbeatBudget(t *testing.T) {
	for _, want := range []string{"round trip", "follow up"} {
		if !strings.Contains(MessagingProtocolPrompt, want) {
			t.Errorf("MessagingProtocolPrompt missing %q", want)
		}
	}
	for _, either := range [][2]string{
		{"budget", "max 32"},
		{"heartbeat", "sparse"},
	} {
		if !strings.Contains(MessagingProtocolPrompt, either[0]) && !strings.Contains(MessagingProtocolPrompt, either[1]) {
			t.Errorf("MessagingProtocolPrompt missing either %q or %q", either[0], either[1])
		}
	}
}
