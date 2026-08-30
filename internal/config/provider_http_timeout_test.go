package config

import (
	"testing"
	"time"
)

// TestProviderHTTPTimeoutCoversRequestBudgets pins the ordering invariant
// behind the derived http.Client wall: the wall must sit above every
// configured per-request budget plus the margin, so a spent request budget
// reports as its own terminal context deadline and never as a transient
// transport fault (a wall hit looks retryable to the retry layer). Mirrors
// subagent_timeout_ordering_test.go for the compiled defaults.
func TestProviderHTTPTimeoutCoversRequestBudgets(t *testing.T) {
	margin := DefaultHTTPWallMarginSeconds * time.Second

	t.Run("default_config", func(t *testing.T) {
		res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
		if err != nil {
			t.Fatal(err)
		}
		wall := res.ProviderHTTPTimeout
		if wall < res.ChatRequestTimeout+margin {
			t.Fatalf("wall %s < chat request budget %s + margin %s", wall, res.ChatRequestTimeout, margin)
		}
		if sub := ResolvedSubagentRequestTimeout(res.Subagents); wall < sub+margin {
			t.Fatalf("wall %s < subagent request budget %s + margin %s", wall, sub, margin)
		}
		if wall < 15*time.Minute {
			t.Fatalf("wall %s < the 15-minute floor", wall)
		}
	})

	t.Run("subagent_request_900_raises_wall_to_960", func(t *testing.T) {
		res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[subagents]\ndefault_request_timeout_seconds = 900\n")})
		if err != nil {
			t.Fatal(err)
		}
		if res.ProviderHTTPTimeout < 960*time.Second {
			t.Fatalf("wall = %s, want >= 960s (900s budget + 60s margin)", res.ProviderHTTPTimeout)
		}
	})

	t.Run("large_budgets_stay_covered", func(t *testing.T) {
		res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "request_timeout_seconds = 3600\n\n[subagents]\ndefault_request_timeout_seconds = 3600\n")})
		if err != nil {
			t.Fatal(err)
		}
		if want := 3600*time.Second + margin; res.ProviderHTTPTimeout < want {
			t.Fatalf("wall = %s, want >= %s (3600s budget + margin)", res.ProviderHTTPTimeout, want)
		}
	})
}

// TestResolvedSubagentRequestTimeout pins the helper both the wall
// derivation and clichat's per-request deadline resolve through: a positive
// configured value is the deadline, anything else falls back to
// DefaultSubagentRequestTimeoutSec.
func TestResolvedSubagentRequestTimeout(t *testing.T) {
	if got := ResolvedSubagentRequestTimeout(SubagentConfig{}); got != DefaultSubagentRequestTimeoutSec*time.Second {
		t.Fatalf("unset: got %s, want %s", got, time.Duration(DefaultSubagentRequestTimeoutSec)*time.Second)
	}
	if got := ResolvedSubagentRequestTimeout(SubagentConfig{DefaultRequestTimeoutSec: 600}); got != 600*time.Second {
		t.Fatalf("configured: got %s, want 600s", got)
	}
	if got := ResolvedSubagentRequestTimeout(SubagentConfig{DefaultRequestTimeoutSec: -5}); got != DefaultSubagentRequestTimeoutSec*time.Second {
		t.Fatalf("negative: got %s, want default", got)
	}
}
