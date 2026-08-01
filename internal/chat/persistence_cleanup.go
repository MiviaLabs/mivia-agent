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
// Does NOT delete any data - if recovery fails, the chunk files remain
// on disk for manual recovery. No silent data loss.
func recoverOrphanedSession(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "chunk_") && strings.HasSuffix(e.Name(), ".jsonl") {
			present[e.Name()] = true
		}
	}

	if len(present) == 0 {
		// No chunk files - truly empty/interrupted directory. Do NOT delete
		// (user may have data there). Just return false.
		return false
	}

	// Load reads chunk_0000 .. chunk_(ChunkCount-1) by index, so only the
	// contiguous run starting at 0 is recoverable. Counting files instead
	// (as this did) wrote a ChunkCount the loader could not satisfy: a
	// directory holding only chunk_0003 was listed but failed to open.
	// Chunks past a gap are left on disk untouched for manual recovery.
	chunkFiles := contiguousChunkNames(present)
	if len(chunkFiles) == 0 {
		return false
	}

	msgs, oldest, newest, ok := readRecoverableChunks(dir, chunkFiles)
	if !ok {
		return false
	}

	turnCount := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			turnCount++
		}
	}

	meta := sessionMeta{
		Name:         filepath.Base(dir),
		Model:        "unknown", // unknown during recovery - user can still load and continue
		Provider:     "unknown",
		CreatedAt:    oldest,
		UpdatedAt:    newest,
		TurnCount:    turnCount,
		TokenCount:   provider.MessagesTokens(msgs),
		ChunkCount:   len(chunkFiles),
		MessageCount: len(msgs),
	}

	if err := writeMetaJSON(dir, meta); err != nil {
		return false
	}

	return true
}

// contiguousChunkNames returns chunk_0000 upward, stopping at the first gap.
// Load reads chunks by index, so only the run starting at 0 is recoverable;
// counting files instead wrote a ChunkCount the loader could not satisfy.
// Chunks past a gap stay on disk untouched for manual recovery.
func contiguousChunkNames(present map[string]bool) []string {
	var names []string
	for i := 0; ; i++ {
		name := fmt.Sprintf(chunkFileName, i)
		if !present[name] {
			return names
		}
		names = append(names, name)
	}
}

// readRecoverableChunks reads the chunks and reports the age of their data.
// A recovered directory must keep that age: stamping time.Now() sorted stale
// crash leftovers ahead of the genuinely newest session, so resume restored
// the wrong one. ok is false when a chunk is unreadable or nothing in the
// directory is real conversation.
func readRecoverableChunks(dir string, chunkFiles []string) (msgs []provider.Message, oldest, newest time.Time, ok bool) {
	hasContent := false
	for _, cf := range chunkFiles {
		path := filepath.Join(dir, cf)
		chunkMsgs, err := readJSONL(path)
		if err != nil {
			return nil, time.Time{}, time.Time{}, false
		}
		if fi, err := os.Stat(path); err == nil {
			mt := fi.ModTime()
			if oldest.IsZero() || mt.Before(oldest) {
				oldest = mt
			}
			if mt.After(newest) {
				newest = mt
			}
		}
		msgs = append(msgs, chunkMsgs...)
		for _, m := range chunkMsgs {
			if m.Role == provider.RoleUser || m.Role == provider.RoleAssistant {
				hasContent = true
				break
			}
		}
	}
	if len(msgs) == 0 || !hasContent {
		return nil, time.Time{}, time.Time{}, false
	}
	if newest.IsZero() {
		// Could not stat any chunk; fall back to now rather than the zero time.
		oldest, newest = time.Now(), time.Now()
	}
	return msgs, oldest, newest, true
}

// cleanupOrphanedSessions recovers or preserves auto-save session directories
// that have chunk files but no meta.json (interrupted/corrupted saves).
// Previously this deleted orphans - now it recovers them.
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
			// No meta.json - likely an interrupted save. Try to recover by
			// rebuilding meta.json from chunk files. Never delete data.
			if recoverOrphanedSession(filepath.Join(sessionDir, name)) {
				fmt.Fprintf(os.Stderr, "mivia: recovered session %q from interrupted save\n", name)
			}
		}
	}
}
