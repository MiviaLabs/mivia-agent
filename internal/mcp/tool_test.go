package mcp

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// TestComposeToolDescriptionAddsProvenance checks that the composed
// description names the remote tool and its server.
func TestComposeToolDescriptionAddsProvenance(t *testing.T) {
	got, err := composeToolDescription("repository", "read.file", "Returns file lines", 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "MCP tool ") {
		t.Fatalf("composeToolDescription() = %q, want a leading %q", got, "MCP tool ")
	}
	for _, want := range []string{`"read.file"`, `"repository"`, "Returns file lines"} {
		if !strings.Contains(got, want) {
			t.Fatalf("composeToolDescription() = %q, want it to contain %q", got, want)
		}
	}
}

// TestComposeToolDescriptionFillsMissingDescription checks that a blank
// description uses the standard missing-description sentence.
func TestComposeToolDescriptionFillsMissingDescription(t *testing.T) {
	got, err := composeToolDescription("repository", "bare", "   \n\t  ", 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	const missing = "The server provides no description."
	if !strings.Contains(got, missing) {
		t.Fatalf("composeToolDescription() = %q, want it to contain %q", got, missing)
	}
	for _, want := range []string{`"bare"`, `"repository"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("composeToolDescription() = %q, want it to contain %q", got, want)
		}
	}
}

// TestComposeToolDescriptionBoundsComposedTotal checks that the size limit
// applies to the whole composed description.
func TestComposeToolDescriptionBoundsComposedTotal(t *testing.T) {
	long := strings.Repeat("x", 200)
	_, err := composeToolDescription("repository", "bare", long, 120, nil)
	if err == nil {
		t.Fatal("composeToolDescription() accepted an oversized description")
	}
	if !strings.Contains(err.Error(), "MCP tool description exceeds configured limit") {
		t.Fatalf("composeToolDescription() error = %v, want the configured limit message", err)
	}
	if _, err := composeToolDescription("repository", "bare", long, 4096, nil); err != nil {
		t.Fatalf("composeToolDescription() = %v", err)
	}
}

// TestComposeToolDescriptionScrubsRemoteName checks that the composed
// description does not keep control or format characters.
func TestComposeToolDescriptionScrubsRemoteName(t *testing.T) {
	got, err := composeToolDescription("repository", "bad\x01\u202ename", "body", 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got, 0x01) || strings.ContainsRune(got, '\u202E') {
		t.Fatalf("composeToolDescription() = %q, want no control or format characters", got)
	}
	if !strings.Contains(got, `"bad  name"`) {
		t.Fatalf("composeToolDescription() = %q, want the scrubbed remote name", got)
	}
}

// TestComposeToolDescriptionRedactsWholeString checks that redaction applies
// to the whole composed description, not only to the body.
func TestComposeToolDescriptionRedactsWholeString(t *testing.T) {
	policy, err := redact.Compile([]string{"repository"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := composeToolDescription("repository", "read", "body", 4096, policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "repository") {
		t.Fatalf("composeToolDescription() = %q, want the server id to be redacted", got)
	}
}

// TestComposeToolDescriptionEmptyNameIsHarmless checks that an empty remote
// name does not fail the composition.
func TestComposeToolDescriptionEmptyNameIsHarmless(t *testing.T) {
	if _, err := composeToolDescription("repository", "", "body", 4096, nil); err != nil {
		t.Fatalf("composeToolDescription() = %v", err)
	}
}
