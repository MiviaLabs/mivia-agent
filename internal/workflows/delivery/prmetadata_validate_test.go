package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// TestSanitizeAgentTitleChecksRedactedLength pins that the rune-count check
// runs on the value sanitizeAgentTitle actually returns. Redaction can
// lengthen a title (a short secret can expand to a longer placeholder), so a
// title that passes the ceiling check BEFORE redaction can still come out
// over MaxTitleRunes - and GitHub would reject it. A title at exactly
// MaxTitleRunes, ending in a substring the policy redacts to something
// longer, must fail here rather than come back oversized.
func TestSanitizeAgentTitleChecksRedactedLength(t *testing.T) {
	previous := redact.Current()
	policy, err := redact.Compile([]string{`token:a`}, nil, redact.DefaultPlaceholder)
	if err != nil {
		t.Fatalf("compile redaction policy: %v", err)
	}
	redact.SetPolicy(policy)
	t.Cleanup(func() { redact.SetPolicy(previous) })

	const match = "token:a"
	title := strings.Repeat("a", MaxTitleRunes-len(match)) + match
	if n := utf8.RuneCountInString(title); n != MaxTitleRunes {
		t.Fatalf("test setup: title has %d runes, want %d", n, MaxTitleRunes)
	}

	got, err := sanitizeAgentTitle(title)
	if n := utf8.RuneCountInString(got); n > MaxTitleRunes {
		t.Fatalf("sanitizeAgentTitle returned %d runes, want at most %d (redaction lengthened the title): %q", n, MaxTitleRunes, got)
	}
	if err == nil {
		t.Fatalf("sanitizeAgentTitle(%d-rune title that expands past the limit after redaction) = nil error, want a PRMetadataError", MaxTitleRunes)
	}
	if !IsPRMetadataError(err) {
		t.Fatalf("sanitizeAgentTitle error = %v, want a PRMetadataError", err)
	}
}
