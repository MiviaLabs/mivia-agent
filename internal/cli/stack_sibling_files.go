package cli

import (
	"sort"
	"strings"
)

// siblingChunkFiles returns the union of the declared files of every chunk
// except the named one, sorted for a deterministic input digest. The union
// covers the chunks known at admission; later decompose waves are not
// visible to already-admitted chunk runs, which keep the directory
// heuristic inside the engine.
func siblingChunkFiles(chunks map[string]*ChunkPlan, chunkID string) []string {
	seen := make(map[string]bool)
	var files []string
	for id, chunk := range chunks {
		if chunk == nil || id == chunkID {
			continue
		}
		for _, f := range chunk.Files {
			f = strings.TrimSpace(f)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}
