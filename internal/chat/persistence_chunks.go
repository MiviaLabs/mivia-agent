package chat

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// Chunk file layout constants shared by the chunk writer, loaders, and
// cleanup paths.
const (
	// ChunkMessageThreshold is the max messages per chunk file.
	// When saving, if messages exceed this, we split into multiple
	// chunk_XXXX.jsonl files for efficient storage and loading.
	ChunkMessageThreshold = 500

	// chunkFilePattern is the glob pattern for chunk files.
	chunkFilePattern = "chunk_*.jsonl"

	// chunkFileName formats a chunk file name by index.
	chunkFileName = "chunk_%04d.jsonl"
)

// redactReasoningForPersistence returns a deep copy of msgs whose assistant
// ReasoningContent has passed through the process-wide redaction policy. It is
// applied to the bytes written to disk, never to host history: callers keep the
// raw reasoning for provider replay and only persist the redacted copy. The
// policy is read via redact.Current() semantics (redact.Text), which is an
// identity when no policy is installed, so unconfigured workspaces persist
// exactly what they always did.
func redactReasoningForPersistence(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].ToolCalls = append([]provider.ToolCall(nil), msgs[i].ToolCalls...)
		out[i].ReasoningContent = redact.Text(out[i].ReasoningContent)
	}
	return out
}

// renameFile is the rename function used by writeSessionChunks to commit
// staged chunks. Exposed as a package-level variable so tests can inject a
// deterministic mid-swap failure.
var renameFile = os.Rename

func writeSessionChunks(dir string, msgs []provider.Message) (int, error) {
	// The chunk bytes are durable, operator-visible state: redact reasoning
	// before it reaches the file. This covers the Session.Save file fallback
	// (and, idempotently, any store that pre-redacts and delegates here).
	msgs = redactReasoningForPersistence(msgs)
	count := chunkCountFor(len(msgs))
	if count == 0 {
		return 0, nil
	}
	// Stage every chunk first, then back up each existing destination chunk
	// and swap the staged files in. A failure at any point must leave the
	// PREVIOUS snapshot byte-for-byte intact: old chunks are never deleted or
	// renamed over before their replacement is fully staged, and every chunk
	// moved aside is restored on failure (deleting the old chunks up front
	// left meta.json pointing at files that no longer exist - an unloadable
	// session - and renaming over them one by one left a mixed snapshot whose
	// low chunks held new content while meta.json still referenced the old
	// chunk count, which loads without error but corrupts the transcript on
	// every later turn). Stale chunks are removed by the caller only after
	// meta.json commits to the new chunk count.
	staged := make([]string, 0, count)
	backedUp := make([]int, 0, count)
	defer func() {
		for _, tmp := range staged {
			_ = os.Remove(tmp) // no-op once renamed
		}
	}()
	for i := 0; i < count; i++ {
		start, end := i*ChunkMessageThreshold, (i+1)*ChunkMessageThreshold
		if end > len(msgs) {
			end = len(msgs)
		}
		tmp := filepath.Join(dir, fmt.Sprintf(chunkFileName, i)) + ".tmp"
		if err := writeJSONL(tmp, msgs[start:end]); err != nil {
			return 0, fmt.Errorf("write chunk %d: %w", i, err)
		}
		staged = append(staged, tmp)
	}
	// restoreBackups moves every chunk whose destination was set aside back
	// into place. It deliberately uses direct os.Rename, bypassing the
	// injectable renameFile seam, so a test-injected commit failure cannot
	// poison the rollback: destinations are absent (backed up, not yet
	// swapped) or replaced atomically (already swapped), and the caller holds
	// the per-directory sessionIOLock write lock, so no reader can observe a
	// mix of old and new chunks.
	restoreBackups := func() {
		for _, i := range backedUp {
			dst := filepath.Join(dir, fmt.Sprintf(chunkFileName, i))
			_ = os.Rename(dst+".bak", dst)
		}
	}
	// Move every existing destination chunk aside before any staged file is
	// swapped in, so a failure can always put the previous snapshot back.
	for i := 0; i < count; i++ {
		dst := filepath.Join(dir, fmt.Sprintf(chunkFileName, i))
		if _, err := os.Stat(dst); err != nil {
			continue // destination absent (e.g. growing chunk count): nothing to preserve
		}
		if err := renameFile(dst, dst+".bak"); err != nil {
			restoreBackups()
			return 0, fmt.Errorf("backup chunk %d: %w", i, err)
		}
		backedUp = append(backedUp, i)
	}
	for i, tmp := range staged {
		if err := renameFile(tmp, filepath.Join(dir, fmt.Sprintf(chunkFileName, i))); err != nil {
			restoreBackups()
			return 0, fmt.Errorf("commit chunk %d: %w", i, err)
		}
	}
	// Every swap succeeded: drop the backups so the directory holds exactly
	// the new snapshot.
	for _, i := range backedUp {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf(chunkFileName, i)) + ".bak")
	}
	return count, nil
}

// removeStaleChunkFiles removes chunk files whose parsed index is >= keep,
// leaving lower-indexed chunks and any unparseable names untouched. It is
// called only after meta.json has committed to the new chunk count, so a
// failed save never deletes a chunk a load may still reference.
func removeStaleChunkFiles(dir string, keep int) {
	oldChunks, err := filepath.Glob(filepath.Join(dir, chunkFilePattern))
	if err != nil {
		return
	}
	for _, f := range oldChunks {
		var idx int
		if _, err := fmt.Sscanf(filepath.Base(f), "chunk_%d.jsonl", &idx); err != nil {
			continue // unparseable name: leave it untouched
		}
		if idx >= keep {
			_ = os.Remove(f)
		}
	}
}

func chunkCountFor(n int) int {
	if n <= 0 {
		return 0
	}
	if n <= ChunkMessageThreshold {
		return 1
	}
	return (n + ChunkMessageThreshold - 1) / ChunkMessageThreshold
}
