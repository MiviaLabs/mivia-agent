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
	// Stage every chunk first, then swap them in by renaming each staged file
	// over its destination. Old chunks are never deleted before or during the
	// swap: a mid-swap failure must leave the previous snapshot fully loadable
	// (deleting the old chunks up front left meta.json pointing at files that
	// no longer exist - an unloadable session - and truncating a chunk in
	// place left a readable prefix whose trailing tool results were gone,
	// which the API rejects on every later turn). Stale chunks are removed by
	// the caller only after meta.json commits to the new chunk count.
	staged := make([]string, 0, count)
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
	for i, tmp := range staged {
		if err := renameFile(tmp, filepath.Join(dir, fmt.Sprintf(chunkFileName, i))); err != nil {
			return 0, fmt.Errorf("commit chunk %d: %w", i, err)
		}
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
