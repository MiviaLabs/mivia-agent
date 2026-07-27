package chat

import (
	"os"
	"path/filepath"
)

// cleanupOrphanedSessions removes session directories that have chunk files
// but no meta.json (interrupted/corrupted saves). Only affects auto-save
// directories. Named sessions are preserved for manual recovery.
func cleanupOrphanedSessions(sessionDir string) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Only clean up auto-save (__last__) directories to avoid
		// accidentally removing user-named sessions that may be in
		// an incomplete state due to external factors.
		if !IsAutoSaveName(name) {
			continue
		}
		metaPath := filepath.Join(sessionDir, name, metaFileName)
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			// No meta.json — likely an interrupted save. Remove the orphan.
			_ = os.RemoveAll(filepath.Join(sessionDir, name))
		}
	}
}
