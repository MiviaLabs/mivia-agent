package chat

import (
	"strings"
	"time"
)

// Auto-save naming and retention. Which directories are mivia's own snapshots
// - and which of them may be reclaimed - is one concern, kept apart from the
// session read/write path in persistence.go.
func IsAutoSaveName(name string) bool {
	if name == AutoSaveName {
		return true
	}
	rest, ok := strings.CutPrefix(name, AutoSaveName)
	if !ok {
		return false
	}
	rest = strings.TrimPrefix(rest, turnSaveMarker)
	return isAutoSaveStamp(rest)
}

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
	for rest != "" {
		rest = rest[1:]
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

func (s *Session) HasAutoSave() bool {
	if !s.ContextEnabled() {
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
