package config

import (
	"time"
)

// Every timeout budget Resolved carries: the provider stream watchdogs, the
// per-request chat deadline, and the derived http.Client wall that must stay
// above all of them. Seconds-to-Duration conversion routes through
// SaturatingSeconds so a TOML integer cannot overflow the multiply.

// applyTimeoutBudgets resolves every timeout budget on one Resolved value:
// the three [provider] stream watchdog bounds, the [chat] per-request
// deadline, and the derived provider HTTP wall (which must consume the chat
// and subagent budgets, so it resolves last). Split from resolveLoaded to
// keep that function inside the structure gate.
func applyTimeoutBudgets(res *Resolved, file File, subagentCfg SubagentConfig) {
	res.StreamIdleTimeout = resolveTimeoutSeconds(file.Provider.StreamIdleTimeoutSeconds, DefaultStreamIdleTimeoutSeconds)
	res.StreamFirstByteTimeout = resolveTimeoutSeconds(file.Provider.StreamFirstByteTimeoutSeconds, DefaultStreamFirstByteTimeoutSeconds)
	res.StreamContentIdleTimeout = resolveTimeoutSeconds(file.Provider.StreamContentIdleTimeoutSeconds, DefaultStreamContentIdleTimeoutSeconds)
	res.ChatRequestTimeout = resolveTimeoutSeconds(file.Chat.RequestTimeoutSeconds, DefaultChatRequestTimeoutSeconds)
	res.ProviderHTTPTimeout = resolveProviderHTTPTimeout(res.ChatRequestTimeout, subagentCfg)
}

// DefaultStreamIdleTimeoutSeconds, DefaultStreamFirstByteTimeoutSeconds, and
// DefaultStreamContentIdleTimeoutSeconds mirror internal/provider's own
// watchdog defaults (provider.DefaultStreamIdleTimeout /
// DefaultStreamFirstByteTimeout / DefaultStreamContentIdleTimeout).
// Duplicated rather than imported: internal/provider already imports
// internal/config (ollama.go, provider.go), so config importing provider
// back would cycle. A mirror-pin test in internal/provider keeps the two
// sides equal.
const (
	DefaultStreamIdleTimeoutSeconds        = 100
	DefaultStreamFirstByteTimeoutSeconds   = 240
	DefaultStreamContentIdleTimeoutSeconds = 90
)

// DefaultChatRequestTimeoutSeconds is the per-LLM-request context deadline
// for a root AGENT turn when [chat] request_timeout_seconds is unset. It
// keeps the historical 15-minute value (chat.DefaultRequestTimeout mirrors
// it as a fallback for sessions built without config; internal/chat imports
// internal/config, so the mirror lives there, not here).
const DefaultChatRequestTimeoutSeconds = 900

// DefaultProviderHTTPTimeoutSeconds mirrors internal/provider's
// DefaultHTTPTimeout (15 minutes), duplicated for the same import-cycle
// reason as the stream watchdog mirrors above; the mirror-pin test in
// internal/provider keeps them equal. It is the floor of the derived
// http.Client wall. DefaultHTTPWallMarginSeconds is the headroom the wall
// keeps above every configured per-request budget.
const (
	DefaultProviderHTTPTimeoutSeconds = 900
	DefaultHTTPWallMarginSeconds      = 60
)

// resolveProviderHTTPTimeout derives the absolute http.Client wall from
// every configured per-request budget. The wall must stay above each budget
// plus the margin, so a spent budget reports as its own terminal context
// deadline and never as a transient transport fault (a wall hit looks
// retryable to the retry layer). Any new request-budget source must feed
// this max().
func resolveProviderHTTPTimeout(chatRequest time.Duration, subagents SubagentConfig) time.Duration {
	wall := time.Duration(DefaultProviderHTTPTimeoutSeconds) * time.Second
	margin := time.Duration(DefaultHTTPWallMarginSeconds) * time.Second
	if bound := chatRequest + margin; bound > wall {
		wall = bound
	}
	if bound := ResolvedSubagentRequestTimeout(subagents) + margin; bound > wall {
		wall = bound
	}
	return wall
}

// SaturatingSeconds converts a configured seconds count to a Duration,
// saturating at +/- MaxTimeoutSeconds (defaults.go) - the repo's one
// overflow-safety ceiling - so the multiply and any later margin addition
// cannot overflow. A TOML integer can carry up to math.MaxInt64 seconds,
// and a plain multiply by time.Second wraps negative above ~292 years.
// The sign is preserved: callers that treat a negative value as "off" see
// it unchanged. Every config-controlled seconds-to-Duration conversion
// must route through this helper, not a bare multiply.
func SaturatingSeconds(sec int) time.Duration {
	if sec > MaxTimeoutSeconds {
		sec = MaxTimeoutSeconds
	}
	if sec < -MaxTimeoutSeconds {
		sec = -MaxTimeoutSeconds
	}
	return time.Duration(sec) * time.Second
}

// resolveTimeoutSeconds defaults an unset (nil) [provider] timeout knob to
// def seconds, converting the resolved value to a time.Duration once so
// Resolved carries a ready-to-use bound instead of a raw *int.
func resolveTimeoutSeconds(raw *int, def int) time.Duration {
	if raw == nil || *raw <= 0 {
		return SaturatingSeconds(def)
	}
	return SaturatingSeconds(*raw)
}
