package subagents

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// parentMessageBeforeStep builds a BeforeStep hook that drains the parent
// mailbox and injects one framed user-role message for steers. Answers that
// arrive at a step boundary with no parked question also degrade to steers.
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
			// ask is parent-routed referral content (plan 53.04).
			if m.Kind == "steer" || m.Kind == "answer" || m.Kind == "ask" {
				if m.Body != "" {
					steerBodies = append(steerBodies, m.Body)
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

func coordinatorMailboxDrain(ctx context.Context) (runtime.MailboxDrainFunc, bool) {
	return runtime.MailboxDrainFrom(ctx)
}
