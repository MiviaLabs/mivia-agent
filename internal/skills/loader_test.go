package skills

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

type loaderCompleter struct{}

func (loaderCompleter) Name() string { return "test" }
func (loaderCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", nil
}
func (loaderCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "skill-result", nil
}
func (loaderCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "skill-result"}, nil
}

func TestLoadMarkdownRegistersCallableInstructionSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code\n---\nUse evidence only.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadMarkdown(root, loaderCompleter{}, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	if err := reg.RegisterAllAsSubagents(d); err != nil {
		t.Fatal(err)
	}
	result := d.Invoke(context.Background(), runtime.Request{
		ID:    "skill-1",
		Kind:  runtime.Subagent,
		Name:  "review",
		Input: json.RawMessage(`"inspect"`),
	})
	if result.Err != nil || !strings.Contains(string(result.Output), "skill-result") {
		t.Fatalf("result=%s err=%v", result.Output, result.Err)
	}
}

func TestLoadMarkdownMissingDirectoryIsEmpty(t *testing.T) {
	reg, err := LoadMarkdown(filepath.Join(t.TempDir(), "missing"), loaderCompleter{}, "model")
	if err != nil || len(reg.List()) != 0 {
		t.Fatalf("registry=%v err=%v", reg, err)
	}
}

func TestParseMarkdownRequiresCompleteClosingDelimiter(t *testing.T) {
	_, _, _, instructions, err := parseMarkdown([]byte("---\nname: x\n---\n---example\nkeep"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instructions, "---example") || !strings.Contains(instructions, "keep") {
		t.Fatalf("instructions=%q", instructions)
	}
}

func TestParseMarkdownDoesNotTreatPrefixAsFrontmatter(t *testing.T) {
	_, _, _, instructions, err := parseMarkdown([]byte("---example\nkeep"))
	if err != nil || instructions != "---example\nkeep" {
		t.Fatalf("instructions=%q err=%v", instructions, err)
	}
}

func TestParseMarkdownExtractsTriggers(t *testing.T) {
	_, _, triggers, _, err := parseMarkdown([]byte("---\nname: review\ndescription: Review code\ntriggers:\n  - architecture review\n  - design review\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 2 || triggers[0] != "architecture review" || triggers[1] != "design review" {
		t.Fatalf("triggers = %v", triggers)
	}
}

func TestLoadMarkdownInjectsTriggersIntoPrompt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: Review code\ntriggers:\n  - arch review\n  - design review\n---\nUse evidence only.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadMarkdown(root, loaderCompleter{}, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	d := reg.List()[0]
	if len(d.Triggers) != 2 {
		t.Fatalf("expected 2 triggers, got %v", d.Triggers)
	}
	// The prompt should contain "Triggers:" followed by the trigger phrases.
	if !strings.Contains(d.Instructions, "Triggers:") {
		t.Fatalf("prompt missing Triggers section: %q", d.Instructions)
	}
	if !strings.Contains(d.Instructions, "arch review") {
		t.Fatalf("prompt missing trigger 'arch review': %q", d.Instructions)
	}
}

func TestLoadMarkdownHandlesSkillWithoutTriggers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "simple")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: simple\ndescription: Simple skill\n---\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadMarkdown(root, loaderCompleter{}, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	d := reg.List()[0]
	if len(d.Triggers) != 0 {
		t.Fatalf("expected 0 triggers, got %v", d.Triggers)
	}
	// Prompt should not contain "Triggers:".
	if strings.Contains(d.Instructions, "Triggers:") {
		t.Fatalf("prompt should not contain Triggers: %q", d.Instructions)
	}
}

func TestLoadMarkdownRejectsMalformedAndOversizedSkills(t *testing.T) {
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	if err := os.Mkdir(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\nname: bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMarkdown(root, loaderCompleter{}, "model"); err == nil {
		t.Fatal("malformed frontmatter accepted")
	}
	root = t.TempDir()
	largeDir := filepath.Join(root, "large")
	if err := os.Mkdir(largeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(largeDir, "SKILL.md"), make([]byte, maxSkillBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMarkdown(root, loaderCompleter{}, "model"); err == nil {
		t.Fatal("oversized skill accepted")
	}
}

// writeSkill is a helper for the audit-regression tests below.
func writeSkill(t *testing.T, name, content string) *Registry {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := LoadMarkdown(root, loaderCompleter{}, "test-model")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return reg
}

// Pins INV-AG-17: an unrecognised frontmatter key is rejected at load, not
// silently discarded. Regression for a dead-field bug shipped as a dead check.
func TestLoadMarkdownRejectsUnknownFrontmatterKey(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: x\nbogus_key: whatever\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "x", "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMarkdown(root, loaderCompleter{}, "test-model")
	if err == nil {
		t.Fatal("expected unknown frontmatter key to be rejected")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("error must name the offending key, got %v", err)
	}
}

// The model-facing prompt is rendered from Definition.Triggers, so the field
// has a production reader rather than being written and never read.
func TestPromptRendersFromDefinitionTriggers(t *testing.T) {
	def := Definition{Name: "n", Description: "d", Triggers: []string{"alpha", "beta"}}
	got := buildPrompt(def, "body")
	for _, want := range []string{"Triggers:", "alpha", "beta"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt %q missing %q", got, want)
		}
	}
}

// Joined triggers are cut on a rune boundary; a byte slice would leave a
// dangling continuation byte in model-facing text.
func TestTruncateRunesNeverSplitsARune(t *testing.T) {
	s := strings.Repeat("→", 140) // 3-byte runes, 420 bytes
	got := truncateRunes(s, triggersJoinedMax)
	if len(got) > triggersJoinedMax {
		t.Fatalf("truncate exceeded cap: %d", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 rune")
	}
}
