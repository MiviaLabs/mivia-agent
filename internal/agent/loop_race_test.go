//go:build race

package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestExecuteToolsParallelRace exercises concurrent batches against one
// registry and scheduler implementation. It is compiled only by -race so the
// detector checks result slots, cancellation, and capability reads together.
func TestExecuteToolsParallelRace(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&scheduledTestTool{name: "race", class: tools.ExecutionRead, delay: time.Microsecond})
	calls := make([]provider.ToolCall, 8)
	for i := range calls {
		calls[i] = tc(string(rune('a'+i)), "race", `{}`)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := executeToolsParallel(context.Background(), calls, reg, Options{MaxConcurrentTools: 2})
			if len(results) != len(calls) {
				t.Errorf("results=%d, want %d", len(results), len(calls))
			}
		}()
	}
	wg.Wait()
}
