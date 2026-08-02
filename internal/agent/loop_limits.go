package agent

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
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
// When spool is non-nil and the body is truncated, the full original body is
// stored under a content ref granted to principal and the notice names that
// ref for read_output. A store failure omits the ref (INV-AG-10 / INV-CE-07-C).
func capToolResult(result string, maxChars, capabilityMaxBytes int, spool *remainder.Spool, principal string) (string, bool) {
	maxResult := maxChars
	if capabilityMaxBytes > 0 && (maxResult <= 0 || capabilityMaxBytes < maxResult) {
		maxResult = capabilityMaxBytes
	}
	return remainder.CapWithSpool(spool, principal, result, maxResult)
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
