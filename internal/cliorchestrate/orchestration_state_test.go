package cliorchestrate

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestPoolLimitsFromConfigZeroMeansUnlimited pins the [subagents] config
// contract: a zero max_depth/max_fanout means unlimited. The pool primitive
// substitutes DefaultMaxDepth (10) and DefaultMaxFanout (32) for zero, so
// without this mapping the coordinator would reject agent-planned DAGs deeper
// than 10 with "dependency depth exceeds limit" - the documented "0 =
// unlimited" in mivia.toml would be a lie.
func TestPoolLimitsFromConfigZeroMeansUnlimited(t *testing.T) {
	depth, fanout := PoolLimitsFromConfig(config.SubagentConfig{})
	if depth != subagents.Unlimited {
		t.Fatalf("max_depth 0: got %d, want Unlimited (%d)", depth, subagents.Unlimited)
	}
	if fanout != subagents.Unlimited {
		t.Fatalf("max_fanout 0: got %d, want Unlimited (%d)", fanout, subagents.Unlimited)
	}
}

// TestPoolLimitsFromConfigPositiveValuesPassThrough: an explicit cap must
// reach the pool unchanged - unlimited is the default, caps are opt-in.
func TestPoolLimitsFromConfigPositiveValuesPassThrough(t *testing.T) {
	depth, fanout := PoolLimitsFromConfig(config.SubagentConfig{MaxDepth: 5, MaxFanout: 12})
	if depth != 5 || fanout != 12 {
		t.Fatalf("explicit caps must pass through: got depth=%d fanout=%d", depth, fanout)
	}
}

// TestPoolLimitsFromConfigUnlimitedSentinelPassesThrough: -1 (the pool's own
// Unlimited sentinel) also maps to Unlimited.
func TestPoolLimitsFromConfigUnlimitedSentinelPassesThrough(t *testing.T) {
	depth, fanout := PoolLimitsFromConfig(config.SubagentConfig{MaxDepth: subagents.Unlimited, MaxFanout: subagents.Unlimited})
	if depth != subagents.Unlimited || fanout != subagents.Unlimited {
		t.Fatalf("Unlimited sentinel must pass through: got depth=%d fanout=%d", depth, fanout)
	}
}
