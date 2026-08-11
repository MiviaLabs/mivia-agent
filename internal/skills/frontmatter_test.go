package skills

import (
	"strings"
	"testing"
)

func TestParseFrontmatter_EmptyOrNoDelimiter(t *testing.T) {
	// No frontmatter at all → nil result, no error.
	m, err := ParseFrontmatter([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("expected nil for empty input")
	}

	// No opening --- → nil result, no error.
	m, err = ParseFrontmatter([]byte("hello world\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("expected nil when no opening ---")
	}
}

func TestParseFrontmatter_ScalarKeys(t *testing.T) {
	input := []byte("---\nname: review\ndescription: Review code\n---\nbody")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil result")
	}
	if v, ok := m["name"]; !ok || v != "review" {
		t.Fatalf("name = %v (%T)", v, v)
	}
	if v, ok := m["description"]; !ok || v != "Review code" {
		t.Fatalf("description = %v (%T)", v, v)
	}
}

func TestParseFrontmatter_QuotedScalars(t *testing.T) {
	input := []byte("---\nname: \"review\"\ndescription: 'Review code'\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m["name"]; !ok || v != "review" {
		t.Fatalf("name = %v", v)
	}
	if v, ok := m["description"]; !ok || v != "Review code" {
		t.Fatalf("description = %v", v)
	}
}

func TestParseFrontmatter_FlowSequence(t *testing.T) {
	input := []byte("---\ntriggers: [review, audit, check]\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 3 || items[0] != "review" || items[1] != "audit" || items[2] != "check" {
		t.Fatalf("triggers = %v", items)
	}
}

func TestParseFrontmatter_BlockSequence(t *testing.T) {
	input := []byte("---\ntriggers:\n  - review\n  - audit\n  - check\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 3 || items[0] != "review" || items[1] != "audit" || items[2] != "check" {
		t.Fatalf("triggers = %v", items)
	}
}

func TestParseFrontmatter_BlockSequenceMultipleWords(t *testing.T) {
	input := []byte("---\ntriggers:\n  - architecture review\n  - design review\n  - review this plan\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 3 || items[0] != "architecture review" || items[1] != "design review" || items[2] != "review this plan" {
		t.Fatalf("triggers = %v", items)
	}
}

func TestParseFrontmatter_CommentsAndBlankLines(t *testing.T) {
	input := []byte("---\nname: review\n# this is a comment\n\ndescription: Review code\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m["name"]; !ok || v != "review" {
		t.Fatalf("name = %v", v)
	}
	if v, ok := m["description"]; !ok || v != "Review code" {
		t.Fatalf("description = %v", v)
	}
}

func TestParseFrontmatter_CRLF(t *testing.T) {
	input := []byte("---\r\nname: review\r\ndescription: Review code\r\n---\r\n")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m["name"]; !ok || v != "review" {
		t.Fatalf("name = %v", v)
	}
}

func TestParseFrontmatter_Unterminated(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nname: review"))
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("expected unterminated error, got %v", err)
	}
}

func TestParseFrontmatter_ExceedsCap(t *testing.T) {
	size := maxFrontmatterBytes + 1
	data := make([]byte, size)
	copy(data, []byte("---\nname: x\n"))
	// Fill remaining with spaces to exceed cap
	for i := len("---\nname: x\n"); i < size; i++ {
		data[i] = ' '
	}
	_, err := ParseFrontmatter(data)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected exceeds error, got %v", err)
	}
}

func TestParseFrontmatter_NoColon(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nname\n---"))
	if err == nil || !strings.Contains(err.Error(), "no colon") {
		t.Fatalf("expected no-colon error, got %v", err)
	}
}

func TestParseFrontmatter_EmptyKey(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\n: value\n---"))
	if err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("expected empty-key error, got %v", err)
	}
}

func TestParseFrontmatter_UnclosedFlowSequence(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\ntriggers: [a, b\n---"))
	if err == nil || !strings.Contains(err.Error(), "unclosed flow sequence") {
		t.Fatalf("expected unclosed flow error, got %v", err)
	}
}

func TestParseFrontmatterKnown_RejectsUnknownKeys(t *testing.T) {
	input := []byte("---\nname: review\ntriggers: [a, b]\nunknown_key: x\n---")
	known := map[string]bool{"name": true, "triggers": true}
	_, err := ParseFrontmatterKnown(input, known)
	if err == nil || !strings.Contains(err.Error(), "unknown frontmatter key") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestParseFrontmatterKnown_AcceptsKnownKeys(t *testing.T) {
	input := []byte("---\nname: review\ntriggers:\n  - audit\n  - check\n---")
	known := map[string]bool{"name": true, "triggers": true}
	m, err := ParseFrontmatterKnown(input, known)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil")
	}
}

func TestParseFrontmatter_EmptyBlockSequence(t *testing.T) {
	// Block sequence with just "key:" and no items - should produce empty string.
	input := []byte("---\ntriggers:\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := m["triggers"]
	if !ok {
		t.Fatal("expected triggers key")
	}
	if s, ok := v.(string); !ok || s != "" {
		t.Fatalf("triggers = %v (%T), want empty string", v, v)
	}
}

func TestParseFrontmatter_EmptyFlowSequence(t *testing.T) {
	input := []byte("---\ntriggers: []\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 0 {
		t.Fatalf("triggers = %v, want empty", items)
	}
}

func TestParseFrontmatter_FlowSequenceQuotedCommas(t *testing.T) {
	input := []byte("---\ntriggers: [\"review, please\", 'check, again', plain]\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 3 {
		t.Fatalf("triggers = %v, want 3 items", items)
	}
	if items[0] != "review, please" {
		t.Fatalf("triggers[0] = %q, want %q", items[0], "review, please")
	}
	if items[1] != "check, again" {
		t.Fatalf("triggers[1] = %q, want %q", items[1], "check, again")
	}
	if items[2] != "plain" {
		t.Fatalf("triggers[2] = %q, want %q", items[2], "plain")
	}
}

func TestSplitFlowSequence_Empty(t *testing.T) {
	items, err := splitFlowSequence("")
	if err != nil {
		t.Fatal(err)
	}
	if items != nil {
		t.Fatalf("expected nil, got %v", items)
	}
}

func TestSplitFlowSequence_Quoted(t *testing.T) {
	items, err := splitFlowSequence(`"a,b",c,"d,e"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0] != "a,b" || items[1] != "c" || items[2] != "d,e" {
		t.Fatalf("got %v", items)
	}
}

func TestSplitFlowSequence_SingleQuoted(t *testing.T) {
	items, err := splitFlowSequence(`'x,y',z`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0] != "x,y" || items[1] != "z" {
		t.Fatalf("got %v", items)
	}
}

func TestParseFrontmatter_TabsInsteadOfSpaces(t *testing.T) {
	input := []byte("---\ntriggers:\n\t- review\n\t- audit\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := m["triggers"].([]string)
	if !ok {
		t.Fatalf("triggers is %T, want []string", m["triggers"])
	}
	if len(items) != 2 || items[0] != "review" || items[1] != "audit" {
		t.Fatalf("triggers = %v", items)
	}
}

func TestParseFrontmatter_MultipleKeys(t *testing.T) {
	input := []byte("---\nname: full-skill\ndescription: Does everything\ntriggers:\n  - trigger1\n  - trigger2\ntools: [read, write]\n---")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m["name"]; !ok || v != "full-skill" {
		t.Fatalf("name = %v", v)
	}
	if v, ok := m["description"]; !ok || v != "Does everything" {
		t.Fatalf("description = %v", v)
	}
	triggers, ok := m["triggers"].([]string)
	if !ok || len(triggers) != 2 || triggers[0] != "trigger1" || triggers[1] != "trigger2" {
		t.Fatalf("triggers = %v", triggers)
	}
	tools, ok := m["tools"].([]string)
	if !ok || len(tools) != 2 || tools[0] != "read" || tools[1] != "write" {
		t.Fatalf("tools = %v", tools)
	}
}

// §6 requires rejection over guessing: an indented line inside a block
// sequence that is not a list item is a nested map, and must be a hard error
// naming the line. Previously it was silently dropped.
func TestParseFrontmatterRejectsNestedMapInBlockSequence(t *testing.T) {
	in := []byte("---\nname: x\ntriggers:\n  - foo\n  nested: oops\n---\nbody\n")
	_, err := ParseFrontmatter(in)
	if err == nil {
		t.Fatal("expected nested map inside block sequence to be rejected")
	}
	if !strings.Contains(err.Error(), "line 5") {
		t.Fatalf("error must name the line, got %v", err)
	}
}

// An indented line with no enclosing block sequence is also a nested map.
func TestParseFrontmatterRejectsStrayIndentedLine(t *testing.T) {
	in := []byte("---\nname: x\n  stray: y\n---\nbody\n")
	if _, err := ParseFrontmatter(in); err == nil {
		t.Fatal("expected stray indented line to be rejected")
	}
}

// §6 says comments and blank lines are skipped - including between a key and
// its first list item, and between items.
func TestParseFrontmatterSkipsCommentsAndBlanksInBlockSequence(t *testing.T) {
	for name, in := range map[string]string{
		"comment before first item": "---\ntriggers:\n  # note\n  - foo\n  - bar\n---\nbody\n",
		"blank before first item":   "---\ntriggers:\n\n  - foo\n  - bar\n---\nbody\n",
		"comment between items":     "---\ntriggers:\n  - foo\n  # note\n  - bar\n---\nbody\n",
	} {
		m, err := ParseFrontmatter([]byte(in))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		got, _ := m["triggers"].([]string)
		if len(got) != 2 || got[0] != "foo" || got[1] != "bar" {
			t.Fatalf("%s: expected [foo bar], got %v", name, got)
		}
	}
}

// An empty list item is a hard error rather than a silently dropped entry.
func TestParseFrontmatterRejectsEmptyListItem(t *testing.T) {
	if _, err := ParseFrontmatter([]byte("---\ntriggers:\n  -\n---\nbody\n")); err == nil {
		t.Fatal("expected empty list item to be rejected")
	}
}

// Plan 43: a duplicate frontmatter key is a hard error rather than a silent
// last-wins overwrite. A repeated key means the file is ambiguous, and the
// parser's contract is to reject ambiguity over guessing.
func TestParseFrontmatterRejectsDuplicateKey(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\nname: review\nname: audit\n---\nbody\n"))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestParseFrontmatterRejectsDuplicateKeyAcrossForms(t *testing.T) {
	// Scalar then block sequence with the same key.
	if _, err := ParseFrontmatter([]byte("---\ntriggers: [a]\ntriggers:\n  - b\n---\nbody\n")); err == nil {
		t.Fatal("expected duplicate-key error for scalar-then-block")
	}
	// Block sequence then scalar with the same key.
	if _, err := ParseFrontmatter([]byte("---\ntriggers:\n  - a\ntriggers: [b]\n---\nbody\n")); err == nil {
		t.Fatal("expected duplicate-key error for block-then-scalar")
	}
	// Empty scalar then non-empty scalar.
	if _, err := ParseFrontmatter([]byte("---\ndescription:\ndescription: x\n---\nbody\n")); err == nil {
		t.Fatal("expected duplicate-key error for empty-then-nonempty")
	}
}

func TestParseFrontmatterAllowsDistinctKeysAfterBlock(t *testing.T) {
	// Distinct keys around a block sequence must keep working.
	m, err := ParseFrontmatter([]byte("---\nname: review\ntriggers:\n  - a\ndescription: code\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["name"] != "review" || m["description"] != "code" {
		t.Fatalf("parsed map = %#v", m)
	}
}

func TestParseFrontmatterKnownWithClosing(t *testing.T) {
	// Multi-line frontmatter: closing --- is at index 3.
	input := []byte("---\nname: review\ndescription: code\n---\nbody\n")
	m, closing, err := ParseFrontmatterKnownWithClosing(input, map[string]bool{"name": true, "description": true})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil")
	}
	if closing != 3 {
		t.Fatalf("closing index = %d, want 3", closing)
	}
}

func TestParseFrontmatterKnownWithClosingNoFrontmatter(t *testing.T) {
	input := []byte("no frontmatter here")
	m, closing, err := ParseFrontmatterKnownWithClosing(input, map[string]bool{"name": true})
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("expected nil for no-frontmatter input")
	}
	if closing != -1 {
		t.Fatalf("closing = %d, want -1", closing)
	}
}

// §6: a scalar value that unquotes to the empty string is malformed.
// Rejecting beats guessing: an author writing name: "" or description: ”
// produced a silent empty-string value instead of an error.
func TestParseFrontmatterRejectsScalarEmptyAfterUnquote(t *testing.T) {
	for name, in := range map[string]string{
		"double-quoted empty":             "---\nname: \"\"\n---\nbody\n",
		"single-quoted empty":             "---\nname: ''\n---\nbody\n",
		"double-quoted empty description": "---\nname: x\ndescription: \"\"\n---\nbody\n",
		"single-quoted empty description": "---\nname: x\ndescription: ''\n---\nbody\n",
		"double-quoted empty tools value": "---\nname: x\ntools: \"\"\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFrontmatter([]byte(in))
			if err == nil {
				t.Fatal("expected error for empty scalar after unquote")
			}
		})
	}
}

// An omitted key must NOT produce an error. Distinguish omitted from empty.
func TestParseFrontmatterAcceptsOmittedKey(t *testing.T) {
	input := []byte("---\nname: x\n---\nbody\n")
	m, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["description"]; ok {
		t.Fatal("omitted description should not appear in result")
	}
	if m["name"] != "x" {
		t.Fatalf("name = %v, want x", m["name"])
	}
}

// A value whose first byte is a quote delimiter but which has no matching
// closing delimiter is malformed (DC-9 silent corruption / DC-14 interface
// tolerance). The parser previously kept the stray leading quote verbatim,
// silently corrupting names and triggers instead of erroring.
func TestParseFrontmatter_RejectsUnbalancedQuoteScalar(t *testing.T) {
	for name, in := range map[string]string{
		"double-quoted scalar": "---\nname: \"unclosed\n---\nbody\n",
		"single-quoted scalar": "---\nname: 'unclosed\n---\nbody\n",
		"bare double quote":    "---\nname: \"\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFrontmatter([]byte(in))
			if err == nil {
				t.Fatal("expected error for unbalanced quote in scalar value")
			}
		})
	}
}

func TestParseFrontmatter_RejectsUnbalancedQuoteFlowSequence(t *testing.T) {
	for name, in := range map[string]string{
		"double-quoted item": "---\ntriggers: [\"a,b]\n---\nbody\n",
		"single-quoted item": "---\ntriggers: ['a, b]\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFrontmatter([]byte(in))
			if err == nil {
				t.Fatal("expected error for unbalanced quote in flow sequence")
			}
		})
	}
}

func TestParseFrontmatter_RejectsUnbalancedQuoteBlockItem(t *testing.T) {
	_, err := ParseFrontmatter([]byte("---\ntriggers:\n  - 'unclosed\n---\nbody\n"))
	if err == nil {
		t.Fatal("expected error for unbalanced quote in block sequence item")
	}
}

// The fix must not over-reject: balanced delimiters, interior quotes, and the
// existing empty-after-unquote rejection keep their behavior.
func TestParseFrontmatter_KeepsBalancedQuotes(t *testing.T) {
	for name, in := range map[string]string{
		"double-quoted scalar": "---\nname: \"closed\"\n---\nbody\n",
		"single-quoted scalar": "---\nname: 'closed'\n---\nbody\n",
		"flow sequence quoted": "---\ntriggers: [\"a,b\", 'c']\n---\nbody\n",
		"interior quotes":      "---\nname: he said \"hi\"\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, err := ParseFrontmatter([]byte(in))
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
			if m == nil {
				t.Fatalf("%s: expected non-nil result", name)
			}
		})
	}
	// The existing empty-after-unquote rejection must still error.
	if _, err := ParseFrontmatter([]byte("---\nname: \"\"\n---\nbody\n")); err == nil {
		t.Fatal("expected empty-after-unquote scalar to be rejected")
	}
}
