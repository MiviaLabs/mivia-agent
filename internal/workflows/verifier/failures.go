package verifier

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// contextErrorFromRun unwraps the context error that a sandboxed run wraps in
// a host-class failure. runSandboxedCommand returns hostFailure(ctx.Err())
// when the caller deadline or cancel ends the run, so the profiles must
// surface the underlying context error instead of reporting a fabricated host
// failure; the controller then settles the run as timed_out.
func contextErrorFromRun(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return nil
}

// Bounds for the structured failure list. The list must stay small enough to
// always fit a repair step's failed_evidence binding, even when the raw
// diagnostic (Detail) is truncated at maxVerifierDiagnosticBytes.
const (
	maxFailureLines     = 20
	maxFailureLineBytes = 400
)

// failureLinePatterns are language-agnostic markers for lines that name a
// failure: test-runner FAIL lines, compiler diagnostics, and assertion
// messages. They are deliberately not tied to Go or to this repository, so
// the extraction works for any project's gate output.
var failureLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^--- FAIL:`),           // go test
	regexp.MustCompile(`^FAIL([ \t]|$)`),       // go test summary line
	regexp.MustCompile(`^FAILED`),              // pytest
	regexp.MustCompile(`^Error:`),              // many toolchains
	regexp.MustCompile(`^ERROR:`),              // many toolchains
	regexp.MustCompile(`^error:`),              // rust, gcc
	regexp.MustCompile(`^panic:`),              // go, python
	regexp.MustCompile(`^cannot `),             // go, rust compile diagnostics
	regexp.MustCompile(`^undefined:`),          // go compile diagnostics
	regexp.MustCompile(`^# `),                  // go package compile header
	regexp.MustCompile(`^\s*\S+:\d+:`),         // path:line: compile/assertion diagnostics
	regexp.MustCompile(`exit status`),          // go test exit line
	regexp.MustCompile(`Tests run:.*Failures`), // junit/maven
	regexp.MustCompile(`AssertionError`),       // python
	regexp.MustCompile(`Assertion failed`),     // c/c++
	// "HARD <check>: <path> ... <fix hint>" is the severity-prefix
	// convention structural gates use (e.g. check_go_structure.py's HARD
	// comment block/file LOC/function LOC lines). Matching it explicitly
	// guarantees a hard violation's file, line range, and repair
	// instruction always reach the repair step's failed_evidence, instead
	// of depending on the output-tail fallback below - which silently
	// drops the hint whenever enough distinct WARN/NOTE lines from the
	// same run push it out of the last maxFailureLines entries.
	regexp.MustCompile(`^HARD `),
}

// extractFailures returns a bounded list of failure diagnostic lines from a
// gate command's output. It redacts the output first, then keeps lines that
// match language-agnostic failure markers. When nothing matches, it falls
// back to the last non-empty lines of the output, where runners usually print
// their summary. The result is capped so it always fits the evidence binding;
// it is complete even when the raw Detail is truncated.
func extractFailures(output []byte) []string {
	text := redact.Text(string(output))
	lines := strings.Split(text, "\n")
	var failures []string
	seen := make(map[string]bool, maxFailureLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !matchesFailureMarker(line) {
			continue
		}
		entry := boundFailureLine(trimmed)
		if seen[entry] {
			continue
		}
		seen[entry] = true
		failures = append(failures, entry)
		if len(failures) >= maxFailureLines {
			break
		}
	}
	if len(failures) == 0 {
		// No marker matched: fall back to the output tail, restored to
		// original order, because runners print their summary at the end.
		for i := len(lines) - 1; i >= 0 && len(failures) < maxFailureLines; i-- {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			failures = append(failures, boundFailureLine(trimmed))
		}
		for i, j := 0, len(failures)-1; i < j; i, j = i+1, j-1 {
			failures[i], failures[j] = failures[j], failures[i]
		}
	}
	return failures
}

func matchesFailureMarker(line string) bool {
	for _, pattern := range failureLinePatterns {
		if pattern.MatchString(line) {
			return true
		}
	}
	return false
}

func boundFailureLine(line string) string {
	runes := []rune(line)
	if len(runes) > maxFailureLineBytes {
		return string(runes[:maxFailureLineBytes]) + "…"
	}
	return line
}

// sourceCommandFailure builds the repairable failure record for a sandboxed
// gate command: the bounded raw diagnostic plus the structured failure list
// extracted from the full output before bounding.
func sourceCommandFailure(raw string, err error) *commandFailure {
	return &commandFailure{
		class:    "source",
		detail:   boundedDiagnostic([]byte(raw)),
		failures: extractFailures([]byte(raw)),
		err:      fmt.Errorf("source check failed: %w", err),
	}
}
