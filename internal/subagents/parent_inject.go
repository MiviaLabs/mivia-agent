package subagents

import (
	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// parentMessageBeforeStep builds a BeforeStep hook that drains the parent
// mailbox and injects one framed user-role message for steers. Answers that
// arrive at a step boundary with no parked question also degrade to steers.
//
// Answer-reconciliation design: a parent answer to a parked question unblocks
// the child via the park channel (the post_message tool result). The SAME
// answer is also mailbox-delivered, so in the common parked case it would be
// re-injected at the next step boundary as a stale parent message — harmless
// redundancy. When the asker is NOT parked (e.g. it timed out), there is no
// park channel to unblock; the mailbox answer degrades to a steer (see the
// "answer degrades to steer" branch below) so the answer is not lost.
func parentMessageBeforeStep(drain runtime.MailboxDrainFunc) func() []provider.Message {
	return func() []provider.Message {
		if drain == nil {
			return nil
		}
		pending := drain()
		if len(pending) == 0 {
			return nil
		}
		var steerBodies []string
		for _, m := range pending {
			// answer at step boundary (not parked) degrades to steer;
			// ask is parent-routed referral content (plan 53.04) and must
			// carry message_id so the target can post kind=answer.
			switch m.Kind {
			case "steer", "answer":
				if m.Body != "" {
					steerBodies = append(steerBodies, m.Body)
				}
			case "ask":
				if text := agent.FormatAskInject(m.MessageID, m.Body); text != "" {
					steerBodies = append(steerBodies, text)
				}
			}
		}
		frame := agent.FrameParentMessages(steerBodies)
		if frame == "" {
			return nil
		}
		return []provider.Message{{
			Role:    provider.RoleUser,
			Content: frame,
		}}
	}
}
