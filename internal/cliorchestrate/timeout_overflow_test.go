package cliorchestrate

import (
	"math"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestHandleRetentionSaturatesInsteadOfOverflow pins saturation on an absurd
// [subagents] handle_retention_seconds value. A bare multiply by time.Second
// overflows to a negative Duration, which would expire completed run handles
// immediately instead of retaining them. The pool construction site
// (buildPool's Timeout from default_timeout_seconds) routes through the same
// config.SaturatingSeconds helper.
func TestHandleRetentionSaturatesInsteadOfOverflow(t *testing.T) {
	cfg := config.SubagentConfig{HandleRetentionSeconds: int(math.MaxInt64)}
	if got := orchestrationHandleRetention(cfg); got <= 0 {
		t.Fatalf("orchestrationHandleRetention overflowed to %v; want a positive saturated retention", got)
	}
	if got := orchestrationHandleRetention(config.SubagentConfig{}); got != defaultHandleRetention {
		t.Fatalf("orchestrationHandleRetention zero-value = %v; want default %v", got, defaultHandleRetention)
	}
}
