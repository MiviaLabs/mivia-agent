package hooks

import (
	"strings"
	"testing"
)

// A PostToolUse hook that prints the PreToolUse nested JSON shape
// {"hookSpecificOutput":{"permissionDecision":"deny"}} must NOT block (reactive
// events cannot block by design), but MUST emit a warning naming the shape
// mismatch so the hook author gets a diagnostic.
func TestReactiveEventWarnsOnPreToolUseShapedOutput(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "wrong.sh", `printf '{"hookSpecificOutput":{"permissionDecision":"deny"}}'
exit 0
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./wrong.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("reactive events cannot block; a wrong shape must not set denied")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "PreToolUse") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the PreToolUse shape mismatch, got %v", out.Warnings)
	}
	if !strings.Contains(out.Context, "hookSpecificOutput") {
		t.Fatalf("raw JSON body must be preserved as context, got %q", out.Context)
	}
}

// Same PreToolUse nested shape but with permissionDecision:"allow". Detection
// must be shape-based, not decision-value-based.
func TestReactiveEventWarnsOnPreToolUseAllowShapedOutput(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "wrong.sh", `printf '{"hookSpecificOutput":{"permissionDecision":"allow"}}'
exit 0
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./wrong.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("reactive events cannot block")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "PreToolUse") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the PreToolUse shape mismatch, got %v", out.Warnings)
	}
	if !strings.Contains(out.Context, "hookSpecificOutput") {
		t.Fatalf("raw JSON body must be preserved as context, got %q", out.Context)
	}
}

// A PostToolUse hook printing the correct reactive shape must produce no
// warning. This is a no-regression case.
func TestReactiveEventCorrectShapeProducesNoWarning(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "correct.sh", `printf '{"decision":"block","reason":"tests failed"}'
exit 0
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./correct.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("reactive events cannot block")
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "PreToolUse") {
			t.Fatalf("correct reactive shape must not warn about PreToolUse, got %v", out.Warnings)
		}
	}
	if !strings.Contains(out.Context, "tests failed") {
		t.Fatalf("context must contain the reason, got %q", out.Context)
	}
}

// Full PreToolUse shape with nested fields: none of the nested fields must be
// silently extracted. The raw body is preserved as context and a warning is
// emitted.
func TestReactiveEventPreToolUseShapeWithReasonAndContext(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "full.sh", `printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"policy violation","additionalContext":"see docs"}}'
exit 0
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./full.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("reactive events cannot block")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "PreToolUse") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the PreToolUse shape mismatch, got %v", out.Warnings)
	}
	if !strings.Contains(out.Context, "hookSpecificOutput") {
		t.Fatalf("raw JSON body must be preserved as context, got %q", out.Context)
	}
}

// Minimal PreToolUse shape with only the decision, no reason or context.
func TestReactiveEventPreToolUseShapeWithOnlyDecision(t *testing.T) {
	requirePOSIX(t)
	dir := hookDir(t)
	script(t, dir, "minimal.sh", `printf '{"hookSpecificOutput":{"permissionDecision":"deny"}}'
exit 0
`)
	groups := group(t, dir, "[[hooks]]\nevent = \"PostToolUse\"\n\n  [[hooks.handlers]]\n  type = \"command\"\n  argv = [\"./minimal.sh\"]\n")

	out := runHooks(t, dir, groups, Payload{Event: EventPostToolUse, Tool: "write_file"})
	if out.Denied {
		t.Fatal("reactive events cannot block")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "PreToolUse") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning naming the PreToolUse shape mismatch, got %v", out.Warnings)
	}
	if !strings.Contains(out.Context, "hookSpecificOutput") {
		t.Fatalf("raw JSON body must be preserved as context, got %q", out.Context)
	}
}
