package uiadapter

// Tool-scope adoption notices (plans/adoption-observability-and-post-resume.md).
// A worktree entry whose registry rebuild fails keeps launch-rooted tools;
// a single-slot notice rides the existing out-carry pattern so the click's
// CommandOutcome surfaces WHY, without changing any creator signatures.
// Contract: single-slot like lastCreated - drained by the caller right
// after the creator returns; every locked helper resets it as its first
// statement, so no cross-call leakage.

const (
	// toolScopeNotResolved is emitted when the target directory can no
	// longer be canonicalized post-bind (vanish-before-adoption TOCTOU).
	toolScopeNotResolved = "tool rules stayed on the launch checkout: worktree directory not resolved"
	// toolScopeRebuildFailedPrefix precedes the builder error text when
	// workspace opening or memory wiring fails while the dir still exists.
	toolScopeRebuildFailedPrefix = "tool rules stayed on the launch checkout: registry rebuild failed"
)

// takeToolScopeNotice drains the slot atomically.
func (p *SessionPool) takeToolScopeNotice() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.lastToolScopeNotice
	p.lastToolScopeNotice = ""
	return n
}

// appendToolScope joins an optional tool-scope warning onto the outcome
// sentence without stray fragments.
func appendToolScope(notice, toolScope string) string {
	if toolScope == "" {
		return notice
	}
	if notice == "" {
		return toolScope
	}
	return notice + " " + toolScope
}

// publishToolScopeNotice stores an adoption warning for immediate drainage;
// every creator call overwrites the slot, so nothing leaks across turns.
func (p *SessionPool) publishToolScopeNotice(notice string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastToolScopeNotice = notice
}
