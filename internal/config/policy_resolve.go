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
