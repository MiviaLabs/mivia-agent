package chatsync

import "errors"

// flushOutcome is the ONE decision about a failed push. Every site that used
// to decide on its own - the flushNow switch, the two internal latches in
// handleBadRequest, Stop's direct flush - reads this instead, so "a session
// the server will never accept again" is defined once.
type flushOutcome int

const (
	// outcomeRetry keeps the batch at the outbox head and tries again on the
	// jittered schedule: network errors, 5xx, 408, 429, and the sequence-gap
	// 400 that keeps its own rebase path.
	outcomeRetry flushOutcome = iota
	// outcomeStop latches sync terminally: a fatal auth failure, and every
	// 400/413/422 that does not name a sequence problem. The BODY is what the
	// server refused; a new session would refuse it identically, so recovery
	// would spend its whole budget re-posting it and then latch anyway.
	outcomeStop
	// outcomeRecover abandons the remote SESSION and re-attaches the backlog
	// onto a fresh one: it was ended (409), deleted (404), or holds a
	// transcript this outbox can never line up with. The bodies are fine.
	outcomeRecover
)

func (o flushOutcome) String() string {
	switch o {
	case outcomeStop:
		return "stop"
	case outcomeRecover:
		return "recover"
	default:
		return "retry"
	}
}

// classifyFlushError maps a FlushOutbox error to its outcome.
func classifyFlushError(err error) flushOutcome {
	switch {
	case err == nil:
		return outcomeRetry
	case errors.Is(err, ErrAuthStop):
		return outcomeStop
	case errors.Is(err, ErrBadRequest):
		var bad *BadRequestError
		if errors.As(err, &bad) && bad.IsSequenceComplaint() {
			return outcomeRetry
		}
		return outcomeStop
	case errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound), errors.Is(err, ErrTranscriptConflict):
		return outcomeRecover
	default:
		return outcomeRetry
	}
}
