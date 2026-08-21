package cli

import (
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
)

// siblingChunkFiles returns the union of the declared files of every chunk
// except the named one, sorted for a deterministic input digest (see
// delivery.SiblingFiles).
func siblingChunkFiles(chunks map[string]*ChunkPlan, chunkID string) []string {
	return delivery.SiblingFiles(chunks, chunkID)
}
