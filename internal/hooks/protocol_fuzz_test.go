package hooks

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzParseStdout guards the protocol layer's core invariants against arbitrary
// hook stdout: parseStdout must never panic on any event, and any deny reason
// it produces stays within maxReasonBytes plus the fixed truncation notice.
// Hook stdout is hook-controlled, so this is the input surface of a security
// gate: a panic or an unbounded reason would be a gate that misbehaves on the
// exact input it exists to police. noVerdictOutcome is fuzzed to the same
// bound: its PreToolUse deny reason is equally model-visible (it carries the OS
// start error verbatim, which embeds the resolved program path), so it must
// obey the identical cap (H-1).
func FuzzParseStdout(f *testing.F) {
	seeds := []string{
		// Valid decisions across the flat and nested shapes.
		`{"decision":"allow"}`,
		`{"decision":"deny","reason":"no"}`,
		`{"decision":"block","reason":"x"}`,
		`{"hookSpecificOutput":{"permissionDecision":"allow"}}`,
		`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no"}}`,
		// Unsupported decisions must deny, not allow.
		`{"hookSpecificOutput":{"permissionDecision":"ask"}}`,
		`{"hookSpecificOutput":{"permissionDecision":"defer"}}`,
		// A complete decision followed by trailing garbage: with truncated=true
		// the capture bound cut only chatter AFTER the decision, and the
		// decision inside the captured bytes must be honored rather than denied
		// (hooks-truncated-allow-denied).
		`{"hookSpecificOutput":{"permissionDecision":"allow"}} trailing`,
		`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no"}} trailing`,
		// Plain text, empty, and malformed bodies.
		`plain text hook output`,
		``,
		`{`,
		`not json at all`,
		`{"decision":`,
		`{"hookSpecificOutput":{"permissionDecision":`,
		// A reason the size of an over-long program path: exercises the bound
		// immediately rather than waiting for the fuzzer to find a long input.
		strings.Repeat("x", maxReasonBytes*2),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		boundNotice := fmt.Sprintf("\n... truncated at %d bytes", maxReasonBytes)
		for _, event := range []Event{EventPreToolUse, EventPostToolUse, EventStop} {
			// Toggle the capture-cut flag so the truncated-deny branch in
			// parsePreToolUse is fuzzed against the same invariants: no panic
			// on any event, and any deny reason stays within maxReasonBytes
			// plus the fixed truncation notice.
			for _, truncated := range []bool{false, true} {
				v := parseStdout(event, execution{program: "/tmp/h.sh", stdout: []byte(body), truncated: truncated})
				if v.denied && len(v.reason) > maxReasonBytes+len(boundNotice) {
					t.Fatalf("%s: deny reason length %d exceeds the bound", event, len(v.reason))
				}
			}
			// noVerdictOutcome is the other model-visible reason producer: a
			// hook that could not start or was killed resolves through it, and
			// its PreToolUse deny reason must obey the same bound as every
			// parsed deny reason (H-1). On reactive events it never denies, so
			// the deny assertion only ever applies to the block-default gate.
			v := noVerdictOutcome(event, Handler{OnTimeout: OnTimeoutBlock}, execution{reason: body, program: "/tmp/h.sh"})
			if v.denied && len(v.reason) > maxReasonBytes+len(boundNotice) {
				t.Fatalf("%s: noVerdict deny reason length %d exceeds the bound", event, len(v.reason))
			}
		}
	})
}
