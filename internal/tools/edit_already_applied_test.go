package tools

import (
	"context"
	"strings"
	"testing"
)

// Neither edit tool checks whether new_string is already present before
// applying old_string -> new_string; it only checks old_string still exists.
// When old_string survives as a substring of new_string (the common case for
// an edit that extends an anchor line, e.g. a function signature followed by
// an appended call), a retried or independently re-issued identical edit
// re-applies and duplicates the inserted text. These two tests pin the
// desired invariant - the duplication must not happen - and are expected to
// fail (RED) until the tools gain an idempotency check.

func TestSearchReplaceReapplyingIdenticalEditDoesNotDuplicate(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.go",
		"content": "func foo() {\n\treturn\n}\n",
	})
	edit := map[string]any{
		"path":       "f.go",
		"old_string": "func foo() {",
		"new_string": "func foo() {\n\textra()",
	}
	mustExec(t, reg, "search_replace", edit)
	// Re-issue the identical call: simulates a retried/duplicate delivery,
	// or a second agent independently applying the same fix.
	_, err := reg.Execute(context.Background(), "search_replace", mustJSON(t, edit))
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
	if n := strings.Count(got, "extra()"); n != 1 {
		t.Fatalf("extra() appears %d times after reapplying an already-applied edit, want 1 (second call err=%v, content=%q)", n, err, got)
	}
}

func TestMultiEditReapplyingIdenticalEditDoesNotDuplicate(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.go",
		"content": "func foo() {\n\treturn\n}\n",
	})
	args := map[string]any{
		"path": "f.go",
		"edits": []map[string]any{
			{"old_string": "func foo() {", "new_string": "func foo() {\n\textra()"},
		},
	}
	mustExec(t, reg, "multi_edit", args)
	_, err := reg.Execute(context.Background(), "multi_edit", mustJSON(t, args))
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
	if n := strings.Count(got, "extra()"); n != 1 {
		t.Fatalf("extra() appears %d times after reapplying an already-applied multi_edit, want 1 (second call err=%v, content=%q)", n, err, got)
	}
}

// A file that already contains new_string elsewhere but still contains a live
// old_string must still be edited. Before the fix, the alreadyApplied skip
// keyed only on new_string's presence, so a file that merely contained the
// replacement text (an earlier independent application to another spot, or the
// model pre-filling it) silently dropped the edit with a false "no change
// (edit already applied)" success report. The fixed predicate strips the
// landed new_string occurrences and only skips when no old_string remains
// outside them.
func TestSearchReplaceNewStringPreexistsStillApplies(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.txt",
		"content": "beta_marker already applied elsewhere\nalpha_marker still needs replacing\n",
	})
	out := mustExec(t, reg, "search_replace", map[string]any{
		"path":       "f.txt",
		"old_string": "alpha_marker",
		"new_string": "beta_marker",
	})
	if strings.Contains(out, "no change") {
		t.Fatalf("live edit was skipped as already applied: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if strings.Contains(got, "alpha_marker") {
		t.Fatalf("old_string still present after edit: %q", got)
	}
	if n := strings.Count(got, "beta_marker"); n != 2 {
		t.Fatalf("new_string appears %d times after edit, want 2: %q", n, got)
	}
}

// Same false-positive scenario through multi_edit: the batch must apply a live
// edit whose new_string merely pre-exists, while a retried identical edit
// (old_string only inside the landed new_string) still no-ops.
func TestMultiEditNewStringPreexistsStillApplies(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.txt",
		"content": "beta_marker already applied elsewhere\nalpha_marker still needs replacing\n",
	})
	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "alpha_marker", "new_string": "beta_marker"},
		},
	})
	if strings.Contains(out, "no change") {
		t.Fatalf("live edit was skipped as already applied: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if strings.Contains(got, "alpha_marker") {
		t.Fatalf("old_string still present after edit: %q", got)
	}
	if n := strings.Count(got, "beta_marker"); n != 2 {
		t.Fatalf("new_string appears %d times after edit, want 2: %q", n, got)
	}
}

// alreadyApplied must not treat a file whose contents are exactly the
// replacement text as "already applied" when old_string is a substring of
// new_string. The strip-based predicate removed the whole new_string (and the
// old_string inside it) and concluded the edit had landed, so a live edit on
// a file that merely contained the target text was silently skipped with a
// false "no change (edit already applied)" report.
func TestAlreadyAppliedOldStringSubstringOfNewString(t *testing.T) {
	const oldString, newString = "foo", "foobar"
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		// File contains exactly the target text: the edit never landed.
		{"content is exactly new_string", "foobar", false},
		// The edit landed on "foobar" (foo -> foobar): the leftover "bar"
		// shows original text survived the replacement, so re-issuing no-ops.
		{"landed once", "foobarbar", true},
		// No new_string anywhere: still a live edit.
		{"no new_string present", "foo foo", false},
		// replace_all landed twice, with original text between the edits.
		{"landed twice with leftovers", "foobarbar foobarbar", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alreadyApplied(tc.content, oldString, newString); got != tc.want {
				t.Errorf("alreadyApplied(%q, %q, %q) = %v, want %v", tc.content, oldString, newString, got, tc.want)
			}
		})
	}
}

// Tool-level symptom of the same bug: a file whose contents are exactly the
// replacement text (plus the file's trailing newline) must still be edited,
// not reported as "already applied". old_string ("foo") is a substring of
// new_string ("foobar"), so the old strip-based predicate removed the whole
// file and skipped the live edit with a false success report.
func TestSearchReplaceOldStringSubstringOfNewStringStillApplies(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.txt",
		"content": "foobar\n",
	})
	out := mustExec(t, reg, "search_replace", map[string]any{
		"path":       "f.txt",
		"old_string": "foo",
		"new_string": "foobar",
	})
	if strings.Contains(out, "no change") {
		t.Fatalf("live edit was skipped as already applied: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if got != "foobarbar\n" {
		t.Fatalf("content = %q, want %q", got, "foobarbar\n")
	}
}
