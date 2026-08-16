package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
)

// siblingChunkFiles returns the union of the declared files of every chunk
// except the named one, sorted for a deterministic input digest (see
// stacking.SiblingFiles).
func siblingChunkFiles(chunks map[string]*ChunkPlan, chunkID string) []string {
	return stacking.SiblingFiles(chunks, chunkID)
}
