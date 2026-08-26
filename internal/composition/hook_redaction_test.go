package composition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/hooks"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Truncating a JSON string before redaction produces a string that is no
// longer valid JSON, so a downstream JSON-aware redactor falls onto its
// pattern-only fallback - silently downgrading the strict
// tools.RedactToolArgs() opt-in to bare pattern matching for any input over
// the bound. Redaction must run on the full string first; only the final
// text is cut.
func TestBoundedRedactedInput_RedactsBeforeTruncatingNotAfter(t *testing.T) {
	old := tools.RedactToolArgs()
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(old) })

	body := strings.Repeat("PRIVATE-FILE-BODY ", 40) // > maxHookRunInputBytes
	input, err := json.Marshal(map[string]any{"path": "secrets.txt", "content": body})
	if err != nil {
		t.Fatal(err)
	}

	got := boundedRedactedInput(input)
	if strings.Contains(got, "PRIVATE-FILE-BODY") {
		t.Fatalf("a content body over the truncation bound must still be elided under strict redaction, got %q", got)
	}
	if !strings.Contains(got, "[content") {
		t.Fatalf("strict redaction must elide the content field by size, got %q", got)
	}
}

// The same input, short enough to skip truncation, must already be elided -
// this is the baseline the truncation case must not regress from.
func TestBoundedRedactedInput_RedactsShortInputToo(t *testing.T) {
	old := tools.RedactToolArgs()
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(old) })

	input, err := json.Marshal(map[string]any{"path": "secrets.txt", "content": "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	got := boundedRedactedInput(input)
	if strings.Contains(got, "hello world") {
		t.Fatalf("a content field must be elided under strict redaction, got %q", got)
	}
}

// With RedactToolArgs off, only the pattern policy applies - unchanged
// default behavior, and a no-op with no policy configured.
func TestBoundedRedactedInput_PatternOnlyWhenStrictModeOff(t *testing.T) {
	old := tools.RedactToolArgs()
	tools.SetRedactToolArgs(false)
	t.Cleanup(func() { tools.SetRedactToolArgs(old) })

	input, err := json.Marshal(map[string]any{"path": "secrets.txt", "content": "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	got := boundedRedactedInput(input)
	if !strings.Contains(got, "hello world") {
		t.Fatalf("with strict redaction off, only the pattern policy (empty by default) applies; got %q", got)
	}
}

// hookRunsFor must carry the fix through to the runtime.HookRun the operator
// actually sees - this is the end-to-end shape the earlier unit tests only
// exercise piecewise.
func TestHookRunsFor_AppliesRedactionBeforeTruncation(t *testing.T) {
	old := tools.RedactToolArgs()
	tools.SetRedactToolArgs(true)
	t.Cleanup(func() { tools.SetRedactToolArgs(old) })

	body := strings.Repeat("PRIVATE-FILE-BODY ", 40)
	input, err := json.Marshal(map[string]any{"path": "secrets.txt", "content": body})
	if err != nil {
		t.Fatal(err)
	}
	outcome := hooks.Outcome{Runs: []hooks.Run{{Event: hooks.EventPostToolUse, Program: "fmt.sh", Tool: "write_file"}}}

	runs := hookRunsFor(outcome, input)
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	if strings.Contains(runs[0].Input, "PRIVATE-FILE-BODY") {
		t.Fatalf("HookRun.Input must not carry the unredacted content body, got %q", runs[0].Input)
	}
}
