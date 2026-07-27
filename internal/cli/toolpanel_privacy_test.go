package cli

import (
	"strings"
	"testing"
)

func TestWritePreviewSectionRedactsSensitiveValuesAndCapsLines(t *testing.T) {
	var b strings.Builder
	writePreviewSection(&b, "input", strings.Repeat("line\n", 20)+"api_key=secret-value\nAuthorization: Bearer abc.def", 80, 6, false)
	out := b.String()
	if strings.Contains(out, "secret-value") || strings.Contains(out, "abc.def") {
		t.Fatalf("preview leaked sensitive value: %q", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Fatalf("expected redaction marker: %q", out)
	}
	if !strings.Contains(out, "more") {
		t.Fatalf("expected line cap marker: %q", out)
	}
}
