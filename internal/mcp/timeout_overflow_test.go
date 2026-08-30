package mcp

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestBoundContextSaturatesInsteadOfOverflow pins saturation on an absurd
// per-server timeout_seconds in the MCP configuration. A bare multiply by
// time.Second overflows to a negative Duration, and context.WithTimeout
// with a negative bound is already expired - every call to that server
// would then fail at once. The discovered-tool timeout (wrapRemoteTools)
// routes through the same helper.
func TestBoundContextSaturatesInsteadOfOverflow(t *testing.T) {
	m := Manager{shutdownCtx: context.Background()}
	ctx, cancel := m.boundContext(context.Background(), int(math.MaxInt64))
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("boundContext returned no deadline for a positive timeout")
	}
	if time.Until(deadline) <= 0 {
		t.Fatalf("boundContext armed an already-expired deadline %v", deadline)
	}
}
