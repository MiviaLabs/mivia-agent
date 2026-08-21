package cli

import (
	"time"
)

// statusDetail is the status-bar stepDetail chrome. It always appends the
// context usage - during a turn so growth is visible without opening
// /status, and while idle so the number does not disappear the moment the
// turn finishes (it is the one place a user can see it without a command).
func (m *tuiModel) statusDetail() string {
	if m.compacting {
		return "compacting context…"
	}
	return appendCtxSuffix(m.stepDetail, m.liveCtxPercent())
}

// liveCtxPercent returns the session's context usage percentage, always -
// waiting or idle, so the status bar never blanks it out.
//
// While waiting, once a per-step EventTokenUsage sample has landed this turn
// (m.liveCtxSampled, set by updateFromDrain), that exact live value wins for
// the rest of the turn - this function must not recompute over it, or a
// quiet stretch between provider calls (e.g. a long tool call) would fall
// back to and display the stale, s.Messages-derived pre-turn estimate.
// Before the first sample, ContextUsage() is the only source and is
// throttled to at most one call per 500 ms - it deep-clones messages and
// marshals tool schemas, too expensive for per-frame calls.
//
// While idle, s.Messages is authoritative again (the turn already committed
// its history), so ContextUsage() is read directly on every call: idle
// renders are keypress/resize-driven, not per-frame, so the 500ms throttle
// buys nothing there and would only risk showing a stale number after the
// next turn silently changes it (e.g. /compact, /model).
func (m *tuiModel) liveCtxPercent() int {
	if !m.waiting {
		return m.session.ContextUsage().Percent
	}
	if m.liveCtxSampled {
		return m.cachedCtxPercent
	}
	now := time.Now()
	if now.Sub(m.cachedCtxPercentAt) < 500*time.Millisecond {
		return m.cachedCtxPercent
	}
	m.cachedCtxPercent = m.session.ContextUsage().Percent
	m.cachedCtxPercentAt = now
	return m.cachedCtxPercent
}
