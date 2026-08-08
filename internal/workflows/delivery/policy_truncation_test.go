package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateRenderedValidUTF8 pins that truncateRendered never emits invalid
// UTF-8: a long unbroken token or CJK run of text has no space to break at, so
// the byte fallback must not split a multi-byte rune.
func TestTruncateRenderedValidUTF8(t *testing.T) {
	t.Run("long unbroken CJK token", func(t *testing.T) {
		s := strings.Repeat("日", 200) // 600 bytes, no spaces
		got, err := truncateRendered(s, 500, false)
		if err != nil {
			t.Fatalf("truncateRendered: %v", err)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncated title is not valid UTF-8: %q", got)
		}
		if len(got) > 500 {
			t.Errorf("truncated title %d bytes exceeds 500", len(got))
		}
		if !strings.HasPrefix(s, got) {
			t.Errorf("truncated title %q is not a prefix of the input", got)
		}
	})

	t.Run("emoji token never splits a rune", func(t *testing.T) {
		s := strings.Repeat("🙂", 300) // 4-byte runes, no spaces
		got, err := truncateRendered(s, 1000, false)
		if err != nil {
			t.Fatalf("truncateRendered: %v", err)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncated title is not valid UTF-8: %q", got)
		}
		if len(got) > 1000 {
			t.Errorf("truncated title %d bytes exceeds 1000", len(got))
		}
	})

	t.Run("commit message truncation is rune-safe too", func(t *testing.T) {
		s := strings.Repeat("日", 200) // 600 bytes
		got, err := truncateRendered(s, 500, true)
		if err != nil {
			t.Fatalf("truncateRendered: %v", err)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncated commit message is not valid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("truncated commit message should end with ..., got %q", got)
		}
	})

	t.Run("commit marker never splits a trailing 4-byte rune", func(t *testing.T) {
		// The marker "..." used to be carved out of the END of the rune-safe
		// prefix with a raw slice, so a 4-byte rune ending exactly at maxBytes
		// lost its last 3 bytes and left a dangling lead byte in the commit
		// message (E1, DC-6). The marker bytes must be reserved BEFORE the
		// rune-safe cut.
		s := strings.Repeat("\U0001F642", 40) // 160 bytes of 4-byte runes
		got, err := truncateRendered(s, 100, true)
		if err != nil {
			t.Fatalf("truncateRendered: %v", err)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncated commit message is not valid UTF-8: %q", got)
		}
		if len(got) > 100 {
			t.Errorf("truncated commit message %d bytes exceeds 100", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("truncated commit message should end with ..., got %q", got)
		}
	})
}

// TestRenderCommitMessageMarkerDoesNotSplitRune pins the E1 fix through the
// public API: a commit message whose rune-safe cut ends with a 4-byte rune
// must render valid UTF-8 (the value reaches `git commit -m` verbatim).
func TestRenderCommitMessageMarkerDoesNotSplitRune(t *testing.T) {
	p := Policy{CommitMessageTemplate: "{{ inputs.task }}", MaxCommitMessageBytes: 100}
	got, err := p.RenderCommitMessage(map[string]string{"task": strings.Repeat("\U0001F642", 40)})
	if err != nil {
		t.Fatalf("RenderCommitMessage: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Errorf("commit message is not valid UTF-8: %q", got)
	}
	if len(got) > 100 {
		t.Errorf("commit message %d bytes exceeds MaxCommitMessageBytes 100", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("commit message should end with ..., got %q", got)
	}
}
