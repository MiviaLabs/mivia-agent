package config

import (
	"os"
	"strings"
)

// Policy resolvers: the settings this binary must not invent. Each one turns
// operator configuration into a resolved value and defaults to the permissive
// answer, so a workspace that configures nothing gets this binary's behaviour
// rather than a compiled-in guess.

func resolvePrivacyConfig(p PrivacyConfig) PrivacyConfig {
	if v, ok := os.LookupEnv("MIVIA_REDACT_TOOL_ARGS"); ok {
		p.RedactToolArgs = parseTruthyEnv(v)
	}
	return p
}

// resolveContextConfig normalizes the durable ceilings. A negative value is
// clamped to 0 (uncapped) rather than rejected, because the only thing a
// nonsensical ceiling could otherwise do is refuse to store a turn - which is
// precisely the failure these bounds are configured to avoid.
func resolveContextConfig(c ContextConfig) ContextConfig {
	// Compaction summaries are opt-out. An absent [context.summary] key
	// resolves to enabled, so a workspace that configures nothing still keeps
	// an account of what compaction dropped; only an explicit
	// `enabled = false` turns it off. Normalizing here means every consumer
	// reads a decided value instead of re-deriving the default.
	if c.Summary.Enabled == nil {
		enabled := true
		c.Summary.Enabled = &enabled
	}
	// Normalize the optional provider/model override: lowercase the provider
	// name so it compares against canonical [providers.<name>] keys and the
	// runtime table. Pairing and configured-ness are validated where an error
	// can be returned (resolveLoaded), not here.
	if c.Summary.Provider != nil {
		p := strings.ToLower(strings.TrimSpace(*c.Summary.Provider))
		c.Summary.Provider = &p
	}
	for _, field := range []*int{
		&c.MaxSourceEventBytes, &c.MaxCheckpointBytes, &c.MaxCommitEvents,
		&c.MaxCommitEventBytes, &c.MaxSessionStateBytes, &c.MaxExportBytes,
		&c.SummaryMetadataBytes, &c.CheckpointMetadataBytes,
	} {
		if *field < 0 {
			*field = 0
		}
	}
	return c
}

func parseTruthyEnv(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}
