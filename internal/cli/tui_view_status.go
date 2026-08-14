package cli

import (
	"fmt"
	"time"
)

// statusDetail is the status-bar stepDetail chrome. During an active turn it
// appends the live context usage so context growth is visible without opening
// /status; idle returns stepDetail as-is.
func (m *tuiModel) statusDetail() string {
	if m.compacting {
		return "compacting context…"
	}
	if !m.waiting {
		return m.stepDetail
	}
	return appendCtxSuffix(m.stepDetail, m.liveCtxPercent())
}

// liveCtxPercent returns the session's context usage percentage, throttled to
// at most one ContextUsage() call per 500 ms. Avoids per-frame cost of message
// cloning + tool-schema marshaling while still showing live values during a turn.
func (m *tuiModel) liveCtxPercent() int {
	if !m.waiting {
		return 0
	}
	now := time.Now()
	if now.Sub(m.cachedCtxPercentAt) < 500*time.Millisecond {
		return m.cachedCtxPercent
	}
	m.cachedCtxPercent = m.session.ContextUsage().Percent
	m.cachedCtxPercentAt = now
	return m.cachedCtxPercent
}

func appendCtxSuffix(detail string, percent int) string {
	suffix := fmt.Sprintf("ctx %d%%", percent)
	if detail == "" {
		return suffix
	}
	return detail + " · " + suffix
}
