package clichat

import (
	"context"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agentmsg"
)

// parked_wait.go - shared helpers for the two parked-answer wait sites
// (waitOnParkedAnswer in ask_flow.go and waitForAnswer in messaging_tools.go).

// parkedWaitDuration returns the duration for a parked wait timer: the full
// wait (waitSec x unit) when no context deadline applies or the deadline is
// farther out, otherwise the time remaining until the deadline. The clamp uses
// pure duration comparison (no int() second-flooring) so a sub-second remaining
// deadline still shortens the timer — a near-deadline wait must arm a short
// timer and exit via the clean no_answer JSON, not race ctx.Done() and surface
// the raw context error. unit defaults to time.Second; the ask flow passes its
// test-shrinkable askWaitUnit so tests keep a deterministic timer race.
func parkedWaitDuration(ctx context.Context, waitSec int, unit ...time.Duration) time.Duration {
	u := time.Second
	if len(unit) > 0 {
		u = unit[0]
	}
	wait := time.Duration(waitSec) * u
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem > 0 && rem < wait {
			wait = rem
		}
	}
	return wait
}

// declineReason reports whether body is a system ask-decline sentinel
// (agentmsg.AskDeclinePrefix + reason) delivered by the coordinator when a
// responder task finalizes without answering. A normal peer answer has no
// prefix and yields ok=false; a sentinel yields the stripped reason.
func declineReason(body string) (string, bool) {
	if strings.HasPrefix(body, agentmsg.AskDeclinePrefix) {
		return strings.TrimPrefix(body, agentmsg.AskDeclinePrefix), true
	}
	return "", false
}
