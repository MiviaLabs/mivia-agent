package agent

import (
	"strings"
	"testing"
)

// Hook output is attributed and separated. The model must be able to tell the
// tool's own result from advice a hook produced about it - otherwise a
// formatter's chatter reads as something the tool returned.
func TestHookContextIsAppendedAsAnAttributedBlock(t *testing.T) {
	out := appendHookContext(`{"ok":true}`, "gofmt rewrote 2 files")
	if !strings.HasPrefix(out, `{"ok":true}`) {
		t.Fatalf("the tool result must come first and survive whole, got %q", out)
	}
	if !strings.Contains(out, "gofmt rewrote 2 files") {
		t.Fatalf("hook context was lost, got %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "hook") {
		t.Fatalf("hook output must be attributed to a hook, got %q", out)
	}
}

func TestHookContextAbsentLeavesTheResultByteIdentical(t *testing.T) {
	const result = `{"ok":true}`
	if got := appendHookContext(result, ""); got != result {
		t.Fatalf("no hook context must change nothing, got %q", got)
	}
	if got := appendHookContext(result, "   \n "); got != result {
		t.Fatalf("blank hook context must change nothing, got %q", got)
	}
}

// An empty tool result with hook context must still read as hook output rather
// than as the tool's own answer.
func TestHookContextOnAnEmptyResultIsStillAttributed(t *testing.T) {
	out := appendHookContext("", "lint found 3 issues")
	if !strings.Contains(strings.ToLower(out), "hook") {
		t.Fatalf("attribution must survive an empty result, got %q", out)
	}
}
