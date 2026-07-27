package skills

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	_, _, instructions, err := parseMarkdown([]byte("---\nname: x\n---\n---example\nkeep"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(instructions, "---example") || !strings.Contains(instructions, "keep") {
		t.Fatalf("instructions=%q", instructions)
	}
}

func TestParseMarkdownDoesNotTreatPrefixAsFrontmatter(t *testing.T) {
	_, _, instructions, err := parseMarkdown([]byte("---example\nkeep"))
	if err != nil || instructions != "---example\nkeep" {
		t.Fatalf("instructions=%q err=%v", instructions, err)
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
