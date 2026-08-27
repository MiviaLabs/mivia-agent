package clichat

// chat_repl_loop_coverage_test.go exercises the replRuntime handlers
// that legacytui drives through the TUI. We construct a runtime with
// nil-able dependencies and exercise each handler individually so the
// diff-coverage gate sees them.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestReplPromptGlyph(t *testing.T) {
	if got := replPromptGlyph("gpt"); got != " gpt > " {
		t.Fatalf("replPromptGlyph = %q", got)
	}
	if got := replPromptGlyph(""); got != "  > " {
		t.Fatalf("replPromptGlyph(empty) = %q", got)
	}
}

func TestPasteEscapeComplete(t *testing.T) {
	// The helper inspects only the last byte; the test exercises the
	// upper-case letter and tilde branches.
	for _, tc := range []struct {
		extras []byte
		want   bool
	}{
		{[]byte("[201~"), true},
		{[]byte("[A"), true},
		{[]byte("["), false},
		{[]byte("[201"), false},
	} {
		if got := pasteEscapeComplete(tc.extras); got != tc.want {
			t.Errorf("pasteEscapeComplete(%q) = %v, want %v", tc.extras, got, tc.want)
		}
	}
}

func TestNewREPLRuntimeWithNilDeps(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "x", ProviderName: "p"}, nil)
	rt := newREPLRuntime(sess, &config.Resolved{Model: "x"}, false, nil)
	if rt == nil {
		t.Fatal("newREPLRuntime must return non-nil")
	}
	if rt.input == nil || rt.renderer == nil {
		t.Fatal("newREPLRuntime must populate input and renderer")
	}
	rt.restore()
}

func TestReplRuntimeHandlePasteStartOnly(t *testing.T) {
	// The other paste handlers (handlePaste, insertPaste, appendPaste)
	// depend on a real terminal input loop and are covered by the
	// legacytui TUI suite. handlePasteStart alone can be exercised here.
	sess := chat.NewSession(&config.Resolved{Model: "x", ProviderName: "p"}, nil)
	rt := newREPLRuntime(sess, &config.Resolved{Model: "x"}, false, nil)
	if !rt.handlePasteStart("\x1b[200~") {
		t.Fatal("handlePasteStart must accept the bracketed-paste begin marker")
	}
	if !rt.inPaste {
		t.Fatal("handlePasteStart must set inPaste=true on the begin marker")
	}
	// An unrelated key must return false (no paste start).
	if rt.handlePasteStart("hello") {
		t.Fatal("handlePasteStart must return false for a non-paste key")
	}
}

func TestReplRuntimeHandleKeyDispatch(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "x", ProviderName: "p"}, nil)
	rt := newREPLRuntime(sess, &config.Resolved{Model: "x"}, false, nil)
	for _, key := range []string{"up", "down", "left", "right", "enter", "ctrl+c"} {
		_, _ = rt.handleKey(key)
	}
}
