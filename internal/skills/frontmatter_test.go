package skills

import (
	"testing"
	"strings"
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
	// Block sequence with just "key:" and no items — should produce empty string.
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
	items := splitFlowSequence("")
	if items != nil {
		t.Fatalf("expected nil, got %v", items)
	}
}

func TestSplitFlowSequence_Quoted(t *testing.T) {
	items := splitFlowSequence(`"a,b",c,"d,e"`)
	if len(items) != 3 || items[0] != "a,b" || items[1] != "c" || items[2] != "d,e" {
		t.Fatalf("got %v", items)
	}
}

func TestSplitFlowSequence_SingleQuoted(t *testing.T) {
	items := splitFlowSequence(`'x,y',z`)
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
