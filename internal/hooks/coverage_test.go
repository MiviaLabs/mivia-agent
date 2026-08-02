package hooks

import (
	"strings"
	"testing"
	"time"
)

func TestAppendBoundedPreservesBoundaries(t *testing.T) {
	var body strings.Builder
	if appendBounded(&body, "") || body.Len() != 0 {
		t.Fatal("empty text must not change the context")
	}
	body.WriteString("first")
	if appendBounded(&body, "second") || body.String() != "first\nsecond" {
		t.Fatalf("normal append = %q", body.String())
	}
	body.Reset()
	body.WriteString(strings.Repeat("x", MaxOutputBytes))
	if !appendBounded(&body, "later") || body.Len() != MaxOutputBytes {
		t.Fatal("a full context must report truncation without growing")
	}
	body.Reset()
	body.WriteString(strings.Repeat("x", MaxOutputBytes-1))
	if !appendBounded(&body, "é") || body.Len() != MaxOutputBytes {
		t.Fatalf("bounded unicode append length = %d", body.Len())
	}
}

func TestProtocolEdgeHelpers(t *testing.T) {
	if got := bound("é", 10); got != "é" {
		t.Errorf("bound with oversized limit = %q", got)
	}
	if got := truncateAtRuneBoundary("éx", 1); got != "" {
		t.Errorf("partial rune truncation = %q, want empty", got)
	}
	if warning := warnIf(execution{program: "/tmp/hook.sh"}, "did nothing"); len(warning) != 1 || warning[0] != "hook /tmp/hook.sh did nothing" {
		t.Errorf("warning without stderr = %#v", warning)
	}

	flat := parsePreToolUse(execution{program: "/tmp/hook.sh"}, `{"decision":"deny"}`)
	if !flat.denied || !strings.Contains(flat.reason, "flat") {
		t.Fatalf("flat PreToolUse decision = %#v", flat)
	}
	updated := parseReactive(execution{program: "/tmp/hook.sh"}, `{"updatedInput":{"x":1}}`)
	if len(updated.warnings) != 1 {
		t.Fatalf("reactive updatedInput warnings = %#v", updated.warnings)
	}
}

func TestHookParserDirectInputShapes(t *testing.T) {
	rows, err := handlerRows([]map[string]any{{"argv": []any{"./hook"}}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("typed handler rows = %#v, %v", rows, err)
	}
	if _, err := handlerRows([]any{"not a table"}); err == nil {
		t.Fatal("non-table handler row must fail")
	}
	argv, err := parseArgv([]string{"./hook", "--flag"})
	if err != nil || strings.Join(argv, " ") != "./hook --flag" {
		t.Fatalf("typed argv = %#v, %v", argv, err)
	}
	if _, err := parseTimeout("10", time.Second); err == nil {
		t.Fatal("non-integer timeout must fail")
	}
	if _, err := parseOnTimeout(true, OnTimeoutBlock); err == nil {
		t.Fatal("non-string on_timeout must fail")
	}
	if _, _, err := parseMatcher(12); err == nil {
		t.Fatal("non-string matcher must fail")
	}
}
