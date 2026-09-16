package skills

import (
	"strings"
	"testing"
)

// The tests in this file cover the spec-conformance frontmatter edges that
// the diff-coverage gate flagged: block-scalar header variants (explicit
// indentation digit, doubled chomping, digit after chomping, invalid
// character), a block-scalar body of only blank lines, the nested-map error
// paths, indentOf's all-whitespace line, and fillParsedSkillFrontmatter's
// license/metadata type checks.

func TestParseFrontmatter_BlockScalarExplicitIndent(t *testing.T) {
	input := []byte("---\nname: s\ndescription: |2\n  two spaces deep\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m["description"].(string)
	if !ok {
		t.Fatalf("description is %T, want string", m["description"])
	}
	if strings.TrimRight(got, "\n") != "two spaces deep" {
		t.Fatalf("description = %q, want %q", got, "two spaces deep")
	}
}

func TestParseFrontmatter_BlockScalarHeaderNotAHeader(t *testing.T) {
	// Each header below must be rejected by blockScalarBody and fall back to
	// a plain scalar value, not error out.
	cases := map[string]string{
		"|-+": "chomp after chomp", // two chomping indicators
		"|29": "digit after digit", // second explicit-indent digit
		"|x":  "invalid character",
	}
	for header, why := range cases {
		// A rejected header is stored as a plain scalar; the next line must
		// be a key line, or the parser rejects the frontmatter outright.
		input := []byte("---\ndescription: " + header + "\nname: n\n---")
		m, err := ParseFrontmatter(input)
		if err != nil {
			t.Fatalf("%s (%s): unexpected error %v", header, why, err)
		}
		if got, _ := m["description"].(string); got != header {
			t.Fatalf("%s (%s): description = %q, want the plain scalar %q", header, why, got, header)
		}
	}
}

func TestParseFrontmatter_BlockScalarHeaderChompBeforeDigit(t *testing.T) {
	// The header grammar accepts the chomping indicator and the explicit
	// indentation digit in either order (blockScalarBody's doc contract);
	// pin the chomp-first form parsing as a block scalar, not a plain
	// scalar. Regression for the audit finding that |-2 was rejected.
	input := []byte("---\nname: n\ndescription: |-2\n  two\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m["description"].(string); got != "two" {
		t.Fatalf("description = %q, want %q", got, "two")
	}
}

func TestParseFrontmatter_BlockScalarBodyOnlyBlankLines(t *testing.T) {
	// A body of only blank lines leaves foldBlockScalar's indent unset; it
	// must default to zero and fold to an empty string rather than panic.
	// The block scalar consumes everything to the end of the frontmatter,
	// so it has to be the last key.
	input := []byte("---\nname: n\ndescription: |-\n \n\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m["description"].(string); got != " " {
		// The space-only line is body content under explicit chomping (it is
		// kept verbatim; only trailing newlines are trimmed). What matters
		// here is that the unset-indent default fires instead of a negative
		// indent producing garbage or a panic.
		t.Fatalf("description = %q, want the single body line %q", got, " ")
	}
	if v, _ := m["name"].(string); v != "n" {
		t.Fatalf("name = %q, want %q", v, "n")
	}
}

func TestParseFrontmatter_NestedMapErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"line without a colon", "---\nmetadata:\n  oops no colon\n---", "expected indented"},
		{"duplicate nested key", "---\nmetadata:\n  a: 1\n  a: 2\n---", "duplicate key"},
	}
	for _, tc := range cases {
		_, err := ParseFrontmatter([]byte(tc.input))
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not contain %q", tc.name, err, tc.want)
		}
	}
}

func TestParseFrontmatter_NestedMapBadQuotedValue(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nmetadata:\n  k: \"unterminated\n---"))
	if err == nil {
		t.Fatal("expected an unquote error for an unterminated quoted value")
	}
}

func TestIndentOfAllWhitespaceLine(t *testing.T) {
	// No production caller can hand indentOf an all-whitespace line (blank
	// lines are skipped before both call sites), so the length return is a
	// defensive backstop; pin its behavior directly.
	if got := indentOf(" \t "); got != 3 {
		t.Fatalf("indentOf(%q) = %d, want 3", " \t ", got)
	}
	if got := indentOf("  key: v"); got != 2 {
		t.Fatalf("indentOf(%q) = %d, want 2", "  key: v", got)
	}
}

func TestFillParsedSkillFrontmatterLicenseAndMetadataTypes(t *testing.T) {
	parsed := &parsedSkill{}
	if err := fillParsedSkillFrontmatter(parsed, map[string]any{"license": 123}); err == nil {
		t.Fatal("non-string license must be rejected")
	} else if !strings.Contains(err.Error(), "license must be a string") {
		t.Fatalf("license error = %q", err)
	}
	if err := fillParsedSkillFrontmatter(parsed, map[string]any{"metadata": "not-a-map"}); err == nil {
		t.Fatal("non-map metadata must be rejected")
	} else if !strings.Contains(err.Error(), "metadata must be a map of scalars") {
		t.Fatalf("metadata error = %q", err)
	}
	// Spec-valid forms pass and stay unsurfaced.
	if err := fillParsedSkillFrontmatter(parsed, map[string]any{"license": "MIT", "metadata": map[string]any{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
}
