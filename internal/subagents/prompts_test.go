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

func TestMessagingProtocolPromptKeepsLengthReasonable(t *testing.T) {
	if n := len(MessagingProtocolPrompt); n >= 1200 {
		t.Errorf("MessagingProtocolPrompt is %d bytes; want < 1200", n)
	}
}
