package agent

// Shared fixture/assertion helpers used across this package's test files.
// Originally lived in shape_batch_test.go alongside the tests for the
// now-deleted legacy shapeBatch entry point; kept here because
// shape_batch_refonly_test.go (refOnlyTier, shared with the SDK path via
// shapeOne) and several other still-live test files depend on them.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

const shapeTestPrincipal = "session-shape"

func testSpool(t *testing.T) (*remainder.Spool, *remainder.MemoryStore) {
	t.Helper()
	store := remainder.NewMemoryStore()
	return remainder.NewSpool(store), store
}

// body returns n bytes of deterministic, ref-free filler.
func body(n int) string { return strings.Repeat("x", n) }

// untruncatedPart is what a worker produces for a result that fit its own cap.
func untruncatedPart(text string, cap int) resultParts {
	return resultParts{cappedBody: text, totalN: len(text), effectiveCap: cap}
}

// capPart runs the real pass-1 cap so tests compose against production
// truncation rather than a hand-rolled imitation of it.
func capPart(t *testing.T, spool *remainder.Spool, text string, cap int) resultParts {
	t.Helper()
	capped, ref, truncated := remainder.CapWithSpoolRef(spool, shapeTestPrincipal, text, cap)
	return resultParts{
		cappedBody: capped, refA: ref, totalN: len(text),
		effectiveCap: cap, truncated: truncated,
	}
}

// partialRefPattern matches a ref token that is not a complete reference:
// "ref:output:" followed by fewer than the full digest characters, at end of
// string or before a non-hex character.
var partialRefPattern = regexp.MustCompile(`ref:output:[0-9a-f]{0,63}(?:[^0-9a-f]|$)`)

func assertNoPartialRef(t *testing.T, s string) {
	t.Helper()
	if m := partialRefPattern.FindString(s); m != "" {
		t.Fatalf("shaped body contains a partial content reference %q in tail=%q", m, tailOf(s))
	}
}

func tailOf(s string) string {
	if len(s) <= 220 {
		return s
	}
	return "…" + s[len(s)-220:]
}

// shapeResultsForTest replaces the legacy shapeBatchResults entry point
// (deleted with the legacy engine) for tests that only need shapeOne's
// per-result tiering - ref-only elision, degrade, recut - over a small
// fixed-budget batch, not the full aggregate F6 straddle/status-line
// behavior shapeBatch used to add on top. Every remaining caller passes a
// single result under a generous budget, so this simplified per-result loop
// is faithful for what they actually exercise.
func shapeResultsForTest(results []toolExecResult, opts Options) []string {
	bodies := make([]string, len(results))
	budget := opts.BatchResultBudgetBytes
	if budget <= 0 {
		for i, r := range results {
			bodies[i] = r.result
		}
		return bodies
	}
	env := newShapeEnv(opts.RemainderSpool, opts.SessionID)
	env.refOnlyTools = opts.RefOnlyTools
	remaining := budget
	for i, r := range results {
		parts := r.parts
		parts.toolName = r.toolCall.Function.Name
		body, _, degraded, _ := shapeOne(parts, remaining, 0, env)
		bodies[i] = appendHookContext(body, parts.hookContext)
		if degraded {
			remaining = 0
			continue
		}
		remaining -= len(body)
		if remaining < 0 {
			remaining = 0
		}
	}
	return bodies
}
