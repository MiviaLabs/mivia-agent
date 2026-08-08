package delivery

import "testing"

// A task input is often a multi-line block. The title template interpolates it,
// so the rendered title holds line breaks. Delivery must still publish: a title
// field is one line, and folding is what the caller wants. Before this, the
// render failed and the whole run stopped at delivery_pending.
func TestRenderTitleFoldsMultiLineInput(t *testing.T) {
	p := Policy{TitleTemplate: "feat: {{ inputs.task }}"}
	got, err := p.RenderTitle(map[string]string{"task": "fix the parser\n\nIt drops the last token.\n"})
	if err != nil {
		t.Fatalf("multi-line input must fold, not fail: %v", err)
	}
	want := "feat: fix the parser It drops the last token."
	if got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
}

// A tab is whitespace, so it folds the same way a line break does.
func TestRenderTitleFoldsTabs(t *testing.T) {
	p := Policy{TitleTemplate: "{{ inputs.task }}"}
	got, err := p.RenderTitle(map[string]string{"task": "a\tb"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a b" {
		t.Fatalf("title = %q, want %q", got, "a b")
	}
}

// A control character that is not whitespace still fails. Such a character
// signals corrupt or hostile input, not a multi-line message.
func TestRenderTitleStillRejectsNonWhitespaceControl(t *testing.T) {
	p := Policy{TitleTemplate: "{{ inputs.task }}"}
	if _, err := p.RenderTitle(map[string]string{"task": "a\x00b"}); err == nil {
		t.Fatal("a NUL byte in a title must fail")
	}
}

// A commit message keeps its line breaks. The fold is for single-line fields.
func TestRenderCommitMessageKeepsNewlines(t *testing.T) {
	p := Policy{CommitMessageTemplate: "{{ inputs.task }}"}
	got, err := p.RenderCommitMessage(map[string]string{"task": "subject\n\nbody"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "subject\n\nbody" {
		t.Fatalf("commit message = %q, want the newlines kept", got)
	}
}
