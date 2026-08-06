package localengine

import (
	"fmt"
	"sync"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestAbandonFenceConcurrentMapAccess races abandon/clearAbandon/isAbandoned
// under the race detector. A bare delete of abandoned without f.mu fails this.
func TestAbandonFenceConcurrentMapAccess(t *testing.T) {
	f := newAbandonFence(workflowledger.NewMemoryRepository())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("wfr-%d", i%8)
		wg.Add(3)
		go func(runID string) {
			defer wg.Done()
			f.abandon(runID)
		}(id)
		go func(runID string) {
			defer wg.Done()
			f.clearAbandon(runID)
		}(id)
		go func(runID string) {
			defer wg.Done()
			_ = f.isAbandoned(runID)
		}(id)
	}
	wg.Wait()
}
