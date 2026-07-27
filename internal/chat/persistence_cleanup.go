package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// recoverOrphanedSession attempts to rebuild meta.json for a session
// directory that has chunk files but no meta.json (interrupted save).
// Returns true if recovery succeeded (meta.json was written).
// Does NOT delete any data — if recovery fails, the chunk files remain
// on disk for manual recovery. No silent data loss.
func recoverOrphanedSession(dir string) bool {
	// Check for chunk files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	var chunkFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "chunk_") && strings.HasSuffix(e.Name(), ".jsonl") {
			chunkFiles = append(chunkFiles, e.Name())
		}
	}

	if len(chunkFiles) == 0 {
		// No chunk files — truly empty/interrupted directory. Do NOT delete
		// (user may have data there). Just return false.
		return false
	}

	// Count total messages across all chunks.
	totalMsgs := 0
	hasContent := false
	for _, cf := range chunkFiles {
		msgs, err := readJSONL(filepath.Join(dir, cf))
		if err != nil {
			return false
		}
		totalMsgs += len(msgs)
		if !hasContent {
			for _, m := range msgs {
				if m.Role == provider.RoleUser || m.Role == provider.RoleAssistant {
					hasContent = true
					break
				}
			}
		}
	}

	if totalMsgs == 0 || !hasContent {
		return false // No real content to recover.
	}

	// Count user turns.
	turnCount := 0
	for _, cf := range chunkFiles {
		msgs, err := readJSONL(filepath.Join(dir, cf))
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.Role == provider.RoleUser {
				turnCount++
			}
		}
	}

	// Model is unknown during recovery — user can still load and continue.
	model := "unknown"

	meta := sessionMeta{
		Name:         filepath.Base(dir),
		Model:        model,
		Provider:     "unknown",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		TurnCount:    turnCount,
		TokenCount:   totalMsgs * 50, // rough estimate for metadata purposes
		ChunkCount:   len(chunkFiles),
		MessageCount: totalMsgs,
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		return false
	}

	return true
}

// cleanupOrphanedSessions recovers or preserves auto-save session directories
// that have chunk files but no meta.json (interrupted/corrupted saves).
// Previously this deleted orphans — now it recovers them.
// Never deletes data. Directories with zero content are left alone for
// manual inspection.
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
		// Only operate on auto-save directories to avoid touching user-named sessions.
		if !IsAutoSaveName(name) {
			continue
		}
		metaPath := filepath.Join(sessionDir, name, metaFileName)
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			// No meta.json — likely an interrupted save. Try to recover by
			// rebuilding meta.json from chunk files. Never delete data.
			if recoverOrphanedSession(filepath.Join(sessionDir, name)) {
				fmt.Fprintf(os.Stderr, "mivia: recovered session %q from interrupted save\n", name)
			}
		}
	}
}
