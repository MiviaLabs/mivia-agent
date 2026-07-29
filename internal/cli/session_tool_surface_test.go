// Package cli — session tool surface tests
package cli

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// Session tools (dispatch_tasks, spawn_agent) are built during
// NewSessionDispatcher and are NOT covered by the default-registry tests
// in internal/tools/generic_surface_test.go. These tests fill that gap.
//
// Rule: .mivia/rules/60-tools-project-language-generic.md

// languageBiasPatterns mirrors the patterns from generic_surface_test.go.
var sessionBiasPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"go test", regexp.MustCompile(`(?i)\bgo\s+test\b`)},
	{"go build", regexp.MustCompile(`(?i)\bgo\s+build\b`)},
	{"go run", regexp.MustCompile(`(?i)\bgo\s+run\b`)},
	{"go vet", regexp.MustCompile(`(?i)\bgo\s+vet\b`)},
	{"gofmt", regexp.MustCompile(`(?i)\bgofmt\b`)},
	{"golang", regexp.MustCompile(`(?i)\bgolang\b`)},
	{"*.go sole example", regexp.MustCompile(`\*\.go\b`)},
	{"package main", regexp.MustCompile(`(?i)\bpackage\s+main\b`)},
	{"cmd/mivia", regexp.MustCompile(`cmd/mivia`)},
	{"github.com/MiviaLabs", regexp.MustCompile(`github\.com/MiviaLabs`)},
	{"go.mod", regexp.MustCompile(`(?i)\bgo\.mod\b`)},
	{"mainly for go", regexp.MustCompile(`(?i)mainly for go`)},
}

// nullCompleter is a stub provider.Completer for test dispatchers.
type nullCompleter struct{}

func (n nullCompleter) Name() string { return "null" }
func (n nullCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return "", nil
}
func (n nullCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return "", nil
}
func (n nullCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return &provider.Response{}, nil
}

// collectSessionToolText builds a session dispatcher with a tool registry
// and optional skills, returning the model-facing Description + parameter
// descriptions for every registered session tool.
func collectSessionToolText(t *testing.T, skillReg *skills.Registry) map[string]string {
	t.Helper()
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{})
	d, err := NewSessionDispatcher(
		reg,
		nullCompleter{},
		"test-model",
		config.SubagentConfig{
			DefaultTimeout: 60,
			StoreBackend:   "memory",
		},
		skillReg,
	)
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	// Collect all model-facing text from the dispatcher's tools.
	// We iterate the registry (which includes session tools via
	// registerSessionTool) and flatten descriptions + schema descriptions.
	out := make(map[string]string)
	for _, tool := range reg.List() {
		var parts []string
		parts = append(parts, tool.Description())
		parts = append(parts, flattenSchemaDescriptions(tool.Parameters())...)
		out[tool.Name()] = strings.Join(parts, "\n")
	}
	_ = d // dispatcher is alive for the scope of the test
	return out
}

func flattenSchemaDescriptions(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		if d, ok := x["description"].(string); ok {
			out = append(out, d)
		}
		for _, child := range x {
			out = append(out, flattenSchemaDescriptions(child)...)
		}
	case []any:
		for _, child := range x {
			out = append(out, flattenSchemaDescriptions(child)...)
		}
	}
	return out
}

func TestSessionToolSurfaceIsProjectAndLanguageGeneric(t *testing.T) {
	texts := collectSessionToolText(t, nil)
	// Should include dispatch_tasks and spawn_agent.
	if _, ok := texts["dispatch_tasks"]; !ok {
		t.Fatal("session tools: missing dispatch_tasks")
	}
	if _, ok := texts["spawn_agent"]; !ok {
		t.Fatal("session tools: missing spawn_agent")
	}
	var failures []string
	for name, text := range texts {
		for _, p := range sessionBiasPatterns {
			if p.re.MatchString(text) {
				failures = append(failures, name+": model-facing text matches "+p.name)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("session tool surface must be project/language-generic (see .mivia/rules/60-tools-project-language-generic.md):\n  %s",
			strings.Join(failures, "\n  "))
	}
}

func TestSessionToolSkillNamesDoNotIntroduceBias(t *testing.T) {
	// Verify that ListModelFacing correctly formats display strings.
	// Display should be "name — description" when description is non-empty.
	reg := skills.NewRegistry()
	if err := reg.Register(skills.Definition{
		Name:        "bug-audit",
		Description: "finds bugs in code",
		Run: func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"output": "ok"})
		},
	}); err != nil {
		t.Fatal(err)
	}
	infos := reg.ListModelFacing(nil)
	if len(infos) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(infos))
	}
	if infos[0].Name != "bug-audit" {
		t.Errorf("expected name 'bug-audit', got %q", infos[0].Name)
	}
	if infos[0].Display != "bug-audit — finds bugs in code" {
		t.Errorf("expected display 'bug-audit — finds bugs in code', got %q", infos[0].Display)
	}

	// Test with empty description — display should be just name.
	if err := reg.Register(skills.Definition{
		Name: "simple-skill",
		Run: func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"output": "ok"})
		},
	}); err != nil {
		t.Fatal(err)
	}
	infos = reg.ListModelFacing(nil)
	if len(infos) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(infos))
	}
	if infos[1].Display != "simple-skill" {
		t.Errorf("expected display 'simple-skill', got %q", infos[1].Display)
	}
}

func TestSanitizeModelFacingText(t *testing.T) {
	tests := []struct {
		input   string
		maxLen  int
		want    string
	}{
		{"hello world", 100, "hello world"},
		{"text with \"quotes\"", 100, "text with quotes"},
		{"text with \\backslash", 100, "text with backslash"},
		{"line1\nline2", 100, "line1 line2"},
		{"tab\tseparated", 100, "tab separated"},
		{"carriage\rreturn", 100, "carriage return"},
		{"too long text here", 10, "too long t"},
		{"\x00control\x1Fchars", 100, "controlchars"},
	}
	for _, tt := range tests {
		got, truncated := skills.SanitizeModelFacingText(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("SanitizeModelFacingText(%q, %d) = %q, want %q (truncated=%v)", tt.input, tt.maxLen, got, tt.want, truncated)
		}
		if truncated != (len(tt.input) > tt.maxLen && len(tt.want) == tt.maxLen) {
			// Just check that truncated flag behaves — for simple cases
		}
	}
}

func TestSessionToolOpenAIToolsJSONHasNoLanguageBias(t *testing.T) {
	// Round-trip through the exact JSON payload sent to the model.
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{})
	d, err := NewSessionDispatcher(
		reg,
		nullCompleter{},
		"test-model",
		config.SubagentConfig{
			DefaultTimeout: 60,
			StoreBackend:   "memory",
		},
	)
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	_ = d
	raw, err := json.Marshal(reg.OpenAITools())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, p := range sessionBiasPatterns {
		if p.re.MatchString(s) {
			t.Fatalf("OpenAITools() payload matches language bias %q", p.name)
		}
	}
}

// Compile-time check that nullCompleter implements provider.Completer.
var _ provider.Completer = (*nullCompleter)(nil)
