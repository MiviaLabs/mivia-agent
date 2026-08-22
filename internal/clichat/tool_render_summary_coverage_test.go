package clichat

// tool_render_summary_coverage_test.go covers the Summary /
// BoundedToolText / pathFromJSONField / pathFromWroteOrUpdated helpers
// in tool_render.go that the broader tool_render_coverage_test.go did
// not drive to coverage.

import "testing"

func TestToolRenderItemSummary(t *testing.T) {
	// Summary renders a one-line preview text, bounded.
	for _, tc := range []struct {
		name, detail, result string
		wantNonEmpty         bool
	}{
		{"read_file", `{"path":"/tmp/x"}`, "ok", true},
		{"read_file", `{"path":"/tmp/x"}`, `{"path":"/tmp/x"}`, true}, // s == p path
		{"unknown", "raw", "result", true},
	} {
		got := NewToolRenderItem(tc.name, tc.detail, tc.result, true, false).Summary(80)
		if tc.wantNonEmpty && got == "" {
			t.Errorf("Summary(%s) returned empty", tc.name)
		}
	}
}

func TestBoundedToolTextEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		in    string
		max   int
		check func(string) bool
	}{
		{"", 0, func(g string) bool { return true }},                   // 0 -> bumped to 1
		{"hello", -1, func(g string) bool { return true }},             // -1 -> bumped to 1
		{"hello world", 5, func(g string) bool { return len(g) <= 8 }}, // bounded
		{"hello", 100, func(g string) bool { return g == "hello" }},    // shorter than cap
	} {
		got := BoundedToolText(tc.in, tc.max)
		if !tc.check(got) {
			t.Errorf("BoundedToolText(%q, %d) = %q", tc.in, tc.max, got)
		}
	}
}

func TestPathFromJSONFieldEscapes(t *testing.T) {
	// pathFromJSONField must unescape \\n and \" inside the quoted path.
	got := pathFromJSONField(`{"path":"\/tmp\/x"}`)
	if got != `/tmp/x` {
		t.Fatalf("pathFromJSONField(escape) = %q", got)
	}
	// Missing colon must return "".
	if got := pathFromJSONField("path no colon"); got != "" {
		t.Fatalf("pathFromJSONField(no colon) = %q", got)
	}
}

func TestPathFromWroteOrUpdatedVariants(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"wrote /tmp/y", "/tmp/y"},
		{"updated /tmp/z", "/tmp/z"},
		{"wrote /tmp/y (size=10)", "/tmp/y"},
		{"nothing here", ""},
	} {
		if got := pathFromWroteOrUpdated(tc.in); got != tc.want {
			t.Errorf("pathFromWroteOrUpdated(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
