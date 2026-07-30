package agent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultToolTimeout is the agent-loop budget for tools that do not declare
// a Capability.Timeout. Finite so ordinary tools cannot hang the loop.
const DefaultToolTimeout = 60 * time.Second

// resolveToolCallTimeout chooses the wall-clock budget for one tool call.
//
// When capabilityTimeout > 0 it is authoritative: tools that need longer
// (run_command, dispatch_tasks, delegate) may set a higher budget, and tools
// that need a tighter cap may set a lower one. The default only applies when
// the tool does not declare a capability timeout. A non-positive default
// falls back to DefaultToolTimeout so the loop never waits unbounded.
func resolveToolCallTimeout(defaultTimeout, capabilityTimeout time.Duration) time.Duration {
	if capabilityTimeout > 0 {
		return capabilityTimeout
	}
	if defaultTimeout > 0 {
		return defaultTimeout
	}
	return DefaultToolTimeout
}

// capToolResult applies the tighter of maxChars and capabilityMaxBytes.
func capToolResult(result string, maxChars, capabilityMaxBytes int) (string, bool) {
	maxResult := maxChars
	if capabilityMaxBytes > 0 && (maxResult <= 0 || capabilityMaxBytes < maxResult) {
		maxResult = capabilityMaxBytes
	}
	if maxResult <= 0 || len(result) <= maxResult {
		return result, false
	}
	suffix := fmt.Sprintf("\n... (truncated %d bytes)", len(result)-maxResult)
	if len(suffix) >= maxResult {
		return trimPartialRune(suffix[:maxResult]), true
	}
	// Trim back to a rune boundary: this body is model-visible and persisted,
	// and a split rune is invalid UTF-8 that the JSON encoder silently replaces
	// with U+FFFD (or a strict provider rejects outright).
	return trimPartialRune(result[:maxResult-len(suffix)]) + suffix, true
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence.
func trimPartialRune(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
