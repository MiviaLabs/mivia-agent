package skills

// Frontmatter is author-written text: every malformed shape must be refused
// with a named field rather than silently coerced, and schemas must be
// admitted at load time rather than at first dispatch.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillRootWithFrontmatter(t *testing.T, frontmatter string) (root string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\n" + frontmatter + "---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFrontmatterTriggersAcceptAScalar(t *testing.T) {
	var parsed parsedSkill
	if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"triggers": "one"}); err != nil {
		t.Fatal(err)
	}
	if len(parsed.triggers) != 1 || parsed.triggers[0] != "one" {
		t.Fatalf("triggers = %v, want the single scalar", parsed.triggers)
	}
	parsed = parsedSkill{}
	if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"triggers": ""}); err != nil {
		t.Fatal(err)
	}
	if parsed.triggers != nil {
		t.Fatalf("an empty trigger produced %v", parsed.triggers)
	}
}

func TestFrontmatterRejectsANonBooleanUserInvocable(t *testing.T) {
	var parsed parsedSkill
	err := fillParsedSkillFrontmatter(&parsed, map[string]any{"user-invocable": "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "user-invocable") {
		t.Fatalf("err = %v, want a user-invocable refusal", err)
	}
	for value, want := range map[string]bool{"TRUE": true, " false ": false} {
		parsed = parsedSkill{}
		if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"user-invocable": value}); err != nil {
			t.Fatal(err)
		}
		if parsed.userInvocable != want {
			t.Fatalf("user-invocable %q = %v, want %v", value, parsed.userInvocable, want)
		}
	}
}

func TestFrontmatterPropagatesFieldFailures(t *testing.T) {
	var parsed parsedSkill
	if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"tools": 7}); err == nil ||
		!strings.Contains(err.Error(), "tools") {
		t.Fatalf("tools = %v, want a tools refusal", err)
	}
	if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"output_schema": 7}); err == nil ||
		!strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("output_schema = %v, want an output_schema refusal", err)
	}
	if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"input_schema": 7}); err == nil ||
		!strings.Contains(err.Error(), "input_schema") {
		t.Fatalf("input_schema = %v, want an input_schema refusal", err)
	}
}

func TestParseSkillSchemaJSON(t *testing.T) {
	if got, err := parseSkillSchemaJSON(nil, "output_schema"); got != nil || err != nil {
		t.Fatalf("omitted schema = %v, %v", got, err)
	}
	if got, err := parseSkillSchemaJSON("   ", "output_schema"); got != nil || err != nil {
		t.Fatalf("blank schema = %v, %v", got, err)
	}
	if _, err := parseSkillSchemaJSON(42, "output_schema"); err == nil ||
		!strings.Contains(err.Error(), "must be a JSON object string") {
		t.Fatalf("non-string schema = %v", err)
	}
	if _, err := parseSkillSchemaJSON(`{`, "output_schema"); err == nil ||
		!strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("malformed schema = %v", err)
	}
	if _, err := parseSkillSchemaJSON(`null`, "output_schema"); err == nil ||
		!strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("null schema = %v", err)
	}
	got, err := parseSkillSchemaJSON(`{"type":"object"}`, "output_schema")
	if err != nil || got["type"] != "object" {
		t.Fatalf("schema = %v, %v", got, err)
	}
}

func TestParseSkillToolsRejectsMalformedDeclarations(t *testing.T) {
	if got, err := parseSkillTools(nil); got != nil || err != nil {
		t.Fatalf("omitted tools = %v, %v", got, err)
	}
	if _, err := parseSkillTools("  "); err == nil || !strings.Contains(err.Error(), "empty tool name") {
		t.Fatalf("blank scalar = %v", err)
	}
	if _, err := parseSkillTools(7); err == nil || !strings.Contains(err.Error(), "list of tool names") {
		t.Fatalf("non-list tools = %v", err)
	}
	got, err := parseSkillTools("read_file")
	if err != nil || len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("scalar tool = %v, %v", got, err)
	}
}

func TestLoaderAdmitsSkillSchemas(t *testing.T) {
	good := skillRootWithFrontmatter(t, `output_schema: '{"type":"object"}'`+"\n"+`input_schema: '{"type":"string"}'`+"\n")
	reg, err := loadMarkdown(good)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("x")
	if !ok {
		t.Fatal("skill not registered")
	}
	if def.OutputSchema["type"] != "object" || def.InputSchema["type"] != "string" {
		t.Fatalf("schemas not carried: out=%v in=%v", def.OutputSchema, def.InputSchema)
	}

	for field, frontmatter := range map[string]string{
		"output_schema": `output_schema: '{"$ref":"https://example.com/s.json"}'` + "\n",
		"input_schema":  `input_schema: '{"$ref":"https://example.com/s.json"}'` + "\n",
	} {
		root := skillRootWithFrontmatter(t, frontmatter)
		_, err := loadMarkdown(root)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%s: err = %v, want an admission refusal naming the field", field, err)
		}
	}
}

// A non-string value on a scalar-only field is a hard error naming the field,
// not a silent coercion. Before the fix the bare type assertions dropped the
// wrong-typed value and left the default in place - e.g. a flow-sequence
// user-invocable parsed as []string{"false"}, the assertion failed, and
// user-invocable stayed true.
func TestFrontmatterRejectsNonStringValues(t *testing.T) {
	for _, field := range []string{"name", "description", "argument-hint", "short-description", "user-invocable"} {
		t.Run(field, func(t *testing.T) {
			var parsed parsedSkill
			if err := fillParsedSkillFrontmatter(&parsed, map[string]any{field: []string{"x"}}); err == nil ||
				!strings.Contains(err.Error(), field) {
				t.Fatalf("%s: err = %v, want a refusal naming the field", field, err)
			}
		})
	}
	// The direct regression: the exact value the parser produces for
	// `user-invocable: [false]` must refuse instead of keeping default true.
	t.Run("user-invocable flow sequence regression", func(t *testing.T) {
		var parsed parsedSkill
		if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"user-invocable": []string{"false"}}); err == nil ||
			!strings.Contains(err.Error(), "user-invocable") {
			t.Fatalf("user-invocable: err = %v, want a refusal naming the field", err)
		}
	})
	t.Run("triggers non-string", func(t *testing.T) {
		var parsed parsedSkill
		if err := fillParsedSkillFrontmatter(&parsed, map[string]any{"triggers": 7}); err == nil ||
			!strings.Contains(err.Error(), "triggers") {
			t.Fatalf("triggers: err = %v, want a refusal naming the field", err)
		}
	})
}

// The end-to-end regression: `user-invocable: [false]` must fail the whole
// load, not register the skill as user-invocable (the default that survived
// the silent coercion).
func TestLoaderRejectsFlowSequenceOnScalarField(t *testing.T) {
	root := skillRootWithFrontmatter(t, "user-invocable: [false]\n")
	_, err := loadMarkdown(root)
	if err == nil || !strings.Contains(err.Error(), "user-invocable") {
		t.Fatalf("err = %v, want a load refusal naming user-invocable", err)
	}
}
