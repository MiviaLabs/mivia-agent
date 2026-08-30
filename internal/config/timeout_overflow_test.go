package config

import (
	"math"
	"testing"
	"time"
)

// TestTimeoutSecondsSaturateInsteadOfOverflow pins saturation on absurd
// configured timeout values. A TOML integer can carry up to math.MaxInt64
// seconds; a plain multiply by time.Second then overflows to a negative
// Duration, and the wall derivation adds a margin on top, which can
// overflow even a value that survived the multiply. Every resolved
// timeout must stay positive and the wall must still cover each budget.
func TestTimeoutSecondsSaturateInsteadOfOverflow(t *testing.T) {
	huge := int(math.MaxInt64)

	if got := resolveTimeoutSeconds(&huge, 90); got <= 0 {
		t.Fatalf("resolveTimeoutSeconds overflowed to %v; want a positive saturated bound", got)
	}

	cfg := SubagentConfig{DefaultRequestTimeoutSec: huge}
	sub := ResolvedSubagentRequestTimeout(cfg)
	if sub <= 0 {
		t.Fatalf("ResolvedSubagentRequestTimeout overflowed to %v; want a positive saturated bound", sub)
	}

	chat := resolveTimeoutSeconds(&huge, DefaultChatRequestTimeoutSeconds)
	wall := resolveProviderHTTPTimeout(chat, cfg)
	if wall <= 0 {
		t.Fatalf("resolveProviderHTTPTimeout overflowed to %v; want a positive wall", wall)
	}
	if wall < chat || wall < sub {
		t.Fatalf("wall %v does not cover saturated budgets (chat %v, subagent %v)", wall, chat, sub)
	}

	margin := time.Duration(DefaultHTTPWallMarginSeconds) * time.Second
	if capped := resolveTimeoutSeconds(&huge, 90); capped+margin <= 0 {
		t.Fatalf("saturation ceiling %v leaves no headroom for the %v margin", capped, margin)
	}

	// The exported helper preserves sign without wrapping: a huge negative
	// stays negative (callers treat negative as "off"), never positive.
	if got := SaturatingSeconds(int(math.MinInt64)); got >= 0 {
		t.Fatalf("SaturatingSeconds(MinInt64) = %v; want a negative saturated value", got)
	}
	if got := SaturatingSeconds(huge); got <= 0 {
		t.Fatalf("SaturatingSeconds(MaxInt64) = %v; want a positive saturated value", got)
	}
}

// TestSaturatingSecondsCeilingIsMaxTimeoutSeconds pins the unification of
// the repo's overflow-safety ceilings: SaturatingSeconds saturates at the
// same MaxTimeoutSeconds that bounds EffectiveTimeoutSec, so the codebase
// has exactly one ceiling and the two cannot silently re-fork.
func TestSaturatingSecondsCeilingIsMaxTimeoutSeconds(t *testing.T) {
	want := time.Duration(MaxTimeoutSeconds) * time.Second
	if got := SaturatingSeconds(int(math.MaxInt64)); got != want {
		t.Fatalf("SaturatingSeconds(MaxInt64) = %v; want the MaxTimeoutSeconds ceiling %v", got, want)
	}
	if got := SaturatingSeconds(int(math.MinInt64)); got != -want {
		t.Fatalf("SaturatingSeconds(MinInt64) = %v; want the negative ceiling %v", got, -want)
	}
}
