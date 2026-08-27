package conversation

// filespanel_deleted_test.go covers the deleted-file glyph branch of
// panelFileRow: an entry classified as deleted renders with the "-" glyph.

import (
	"strings"
	"testing"
)

func TestPanelFileRowDeletedGlyph(t *testing.T) {
	scr := panelScreen(t, 80, 24)
	row := scr.panelFileRow(fileEntry{Path: "gone.go", Kind: fileDeleted}, "", false)
	plain := stripAnsiForTest(row)
	if !strings.Contains(plain, "- gone.go") {
		t.Fatalf("deleted row = %q, want the \"-\" glyph before gone.go", plain)
	}
}

// stripAnsiForTest removes ANSI escape sequences so glyph assertions see the
// plain text.
func stripAnsiForTest(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
