package chat

import (
	"strings"
	"time"
)

// Auto-save naming and retention. Which directories are mivia's own snapshots
// - and which of them may be reclaimed - is one concern, kept apart from the
// session read/write path in persistence.go.

// IsAutoSaveName reports whether name is a directory this package minted for
// an auto-save, and therefore one prune is allowed to delete.
//
// A bare prefix test is not enough: the prefix is not reserved at save time, so
// a user typing "/save __last__mywork" produced a name that pruning happily
// deleted once the budget was exceeded. Requiring the full minted shape -
// prefix, optional turn marker, timestamp, optional collision counter - keeps
// user-named sessions out of the prune set while still recognising every name
// (including legacy second-precision ones) already on disk.
func IsAutoSaveName(name string) bool {
	if name == AutoSaveName {
		return true // legacy pre-rolling-save directory
	}
	rest, ok := strings.CutPrefix(name, AutoSaveName)
	if !ok {
		return false
	}
	rest = strings.TrimPrefix(rest, turnSaveMarker)
	return isAutoSaveStamp(rest)
}

// IsTurnSaveName reports whether name is a per-turn crash-recovery snapshot.
func IsTurnSaveName(name string) bool {
	return IsAutoSaveName(name) && strings.Contains(name, turnSaveMarker)
}

// isAutoSaveStamp reports whether s is a timestamp minted by uniqAutoSaveName,
// optionally followed by the "-N" (and "-nano-seq") collision suffixes.
func isAutoSaveStamp(s string) bool {
	stamp, rest := s, ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		stamp, rest = s[:i], s[i:]
	}
	if _, err := time.Parse(autoSaveTimeFormat, stamp); err != nil {
		if _, err := time.Parse(autoSaveLegacyTimeFormat, stamp); err != nil {
			return false
		}
	}
	// Each remaining group must be "-" followed by at least one digit.
	for rest != "" {
		rest = rest[1:] // leading '-', guaranteed by the split and the check below
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 0 {
			return false
		}
		rest = rest[n:]
		if rest != "" && rest[0] != '-' {
			return false
		}
	}
	return true
}

// expiredAutoSaves returns the auto-save sessions that exceed their retention
// budget, oldest first. Exit snapshots and per-turn snapshots have separate
// budgets: previously turn snapshots were excluded from pruning entirely, so
// they accumulated one full transcript copy per turn with nothing to reclaim
// them. keepTurnName is never returned - it is the caller's live rolling
// snapshot, which another process could out-timestamp.
//
// infos must be sorted most-recent first (both List implementations are).
func expiredAutoSaves(infos []SessionInfo, keepTurnName string) []SessionInfo {
	var exits, turns []SessionInfo
	for _, si := range infos {
		switch {
		case si.Name == keepTurnName:
		case IsTurnSaveName(si.Name):
			turns = append(turns, si)
		case IsAutoSaveName(si.Name):
			exits = append(exits, si)
		}
	}
	var expired []SessionInfo
	if len(exits) > AutoSaveKeep {
		expired = append(expired, exits[AutoSaveKeep:]...)
	}
	// TurnSaveKeep >= 1, so the newest turn snapshot always survives.
	if len(turns) > TurnSaveKeep {
		expired = append(expired, turns[TurnSaveKeep:]...)
	}
	return expired
}

// HasAutoSave checks whether any auto-saved session exists on disk.
func (s *Session) HasAutoSave() bool {
	if s.SessionDir == "" {
		return false
	}
	infos, err := s.ListSessions()
	if err != nil {
		return false
	}
	for _, si := range infos {
		if IsAutoSaveName(si.Name) {
			return true
		}
	}
	return false
}

// LatestAutoSaveName returns the name of the most recent auto-save session,
// or empty string if none exist. The bare __last__ name is returned as-is for
// backward compatibility with pre-rolling-save sessions.
func (s *Session) LatestAutoSaveName() string {
	infos, err := s.ListSessions()
	if err != nil {
		return ""
	}
	latest := ""
	var latestTime time.Time
	for _, si := range infos {
		if !IsAutoSaveName(si.Name) {
			continue
		}
		if latest == "" || si.UpdatedAt.After(latestTime) {
			latest = si.Name
			latestTime = si.UpdatedAt
		}
	}
	return latest
}

// pruneAutoSaves removes orphaned auto-saves beyond AutoSaveKeep.
func (s *Session) pruneAutoSaves() {
	if s.SessionDir == "" {
		return
	}
	cleanupOrphanedSessions(s.SessionDir)

	infos, err := s.ListSessions()
	if err != nil {
		return
	}
	s.mu.RLock()
	live := s.turnSaveName
	s.mu.RUnlock()
	for _, si := range expiredAutoSaves(infos, live) {
		_ = s.DeleteSession(si.Name)
	}
}
