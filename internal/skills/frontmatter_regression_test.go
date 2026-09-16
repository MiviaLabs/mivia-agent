package skills

import (
	"testing"
)

// Regression probes for adversarial-review findings.
func TestBlockScalarNonIndentedKeyLineTerminatesScalar(t *testing.T) {
	// A non-indented "key: value" after a block-scalar header terminates the
	// scalar (as in YAML): it must be parsed as a frontmatter key, never
	// silently folded into the scalar body (which would hide it from the
	// duplicate- and unknown-key contracts).
	m, err := ParseFrontmatter([]byte("---\ndescription: >-\n  body\nname: hostile\nlicense: MIT\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["description"] != "body" {
		t.Fatalf("description = %q, want %q (scalar must terminate at the non-indented key)", m["description"], "body")
	}
	if m["name"] != "hostile" || m["license"] != "MIT" {
		t.Fatalf("keys after the block scalar were swallowed: %v", m)
	}
	// Blank and indented lines inside the scalar are still content.
	m, err = ParseFrontmatter([]byte("---\ndescription: |\n  line1\n\n  # comment\n  line3\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["description"] != "line1\n\n# comment\nline3\n" {
		t.Fatalf("indented body misfolded: %q", m["description"])
	}
}

func TestBlockScalarDigitLedScalarIsPlainValue(t *testing.T) {
	// "2|" and "2>" are plain scalars, not block headers.
	m, err := ParseFrontmatter([]byte("---\nversion: 2|\nother: 2>\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["version"] != "2|" || m["other"] != "2>" {
		t.Fatalf("digit-led scalars misclassified: %v", m)
	}
}

func TestBlockScalarKeepsBlankAndHashLines(t *testing.T) {
	m, err := ParseFrontmatter([]byte("---\ndescription: |\n  line1\n\n  # looks like comment\n  line3\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m["description"].(string)
	want := "line1\n\n# looks like comment\nline3\n"
	if got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

func TestBlockScalarFoldedBlankLines(t *testing.T) {
	m, err := ParseFrontmatter([]byte("---\ndescription: >\n  a\n\n  b\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m["description"].(string); got != "a\nb\n" {
		t.Fatalf("folded = %q, want %q", got, "a\nb\n")
	}
}

func TestNestedMapDeepNestingRejected(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nmetadata:\n  author:\n    name: x\n  url: http://e\n---\nbody\n"))
	if err == nil {
		t.Fatal("expected deep nesting to fail closed")
	}
}

func TestNestedMapWithCommentBeforeFirstKey(t *testing.T) {
	m, err := ParseFrontmatter([]byte("---\nmetadata:\n  # provenance\n  author: MiviaLabs\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	md, ok := m["metadata"].(map[string]any)
	if !ok || md["author"] != "MiviaLabs" {
		t.Fatalf("metadata = %v", m["metadata"])
	}
}
