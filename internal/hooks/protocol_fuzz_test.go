package hooks

import (
	"fmt"
	"testing"
)

// FuzzParseStdout guards the protocol layer's core invariants against arbitrary
// hook stdout: parseStdout must never panic on any event, and any deny reason
// it produces stays within maxReasonBytes plus the fixed truncation notice.
// Hook stdout is hook-controlled, so this is the input surface of a security
// gate: a panic or an unbounded reason would be a gate that misbehaves on the
// exact input it exists to police.
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
		// Plain text, empty, and malformed bodies.
		`plain text hook output`,
		``,
		`{`,
		`not json at all`,
		`{"decision":`,
		`{"hookSpecificOutput":{"permissionDecision":`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		for _, event := range []Event{EventPreToolUse, EventPostToolUse, EventStop} {
			v := parseStdout(event, execution{program: "/tmp/h.sh", stdout: []byte(body)})
			if v.denied && len(v.reason) > maxReasonBytes+len(fmt.Sprintf("\n... truncated at %d bytes", maxReasonBytes)) {
				t.Fatalf("%s: deny reason length %d exceeds the bound", event, len(v.reason))
			}
		}
	})
}
