package tools

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Tool OpenAI-facing text must stay project- and language-generic.
// mivia is a host coding agent used in any repo; the tool surface must not
// teach models that this product is Go-only (or any single ecosystem).
//
// Rule: .ai/rules/60-tools-project-language-generic.md

// languageBiasPatterns are substrings/regexes that encode a preferred
// host language or this product's own stack in tool schemas/descriptions.
// Keep this list strict: fixture file names in tests are fine; model-facing
// Description() / parameter "description" fields are not.
var languageBiasPatterns = []struct {
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

func collectModelFacingToolText(t *testing.T) map[string]string {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	out := make(map[string]string)
	for _, tool := range reg.List() {
		var parts []string
		parts = append(parts, tool.Description())
		// Flatten parameter schema descriptions recursively.
		parts = append(parts, flattenSchemaDescriptions(tool.Parameters())...)
		out[tool.Name()] = strings.Join(parts, "\n")
	}
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

func TestToolSurfaceIsProjectAndLanguageGeneric(t *testing.T) {
	texts := collectModelFacingToolText(t)
	if len(texts) == 0 {
		t.Fatal("expected registered tools")
	}
	var failures []string
	for name, text := range texts {
		for _, p := range languageBiasPatterns {
			if p.re.MatchString(text) {
				failures = append(failures, name+": model-facing text matches "+p.name)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("tool surface must be project/language-generic (see .ai/rules/60-tools-project-language-generic.md):\n  %s",
			strings.Join(failures, "\n  "))
	}
}

func TestToolSurfacePreferFilesystemOverRunCommand(t *testing.T) {
	// run_command must present as last resort so agents do not shell for cat/ls/grep.
	texts := collectModelFacingToolText(t)
	run, ok := texts["run_command"]
	if !ok {
		t.Fatal("missing run_command")
	}
	lower := strings.ToLower(run)
	if !strings.Contains(lower, "last resort") {
		t.Fatal("run_command description must say LAST RESORT")
	}
	if !strings.Contains(lower, "argv") {
		t.Fatal("run_command description must mention argv")
	}
	// Prefer multi-ecosystem examples when giving argv illustrations.
	if strings.Contains(lower, `["go"`) && !strings.Contains(lower, "npm") && !strings.Contains(lower, "make") {
		t.Fatal("run_command must not use Go-only argv examples")
	}
}

func TestDefaultAllowlistIsMultiEcosystem(t *testing.T) {
	// Allowlist may include go — that is fine — but must not be Go-only.
	must := []string{"git", "make", "python3", "npm", "node", "cargo", "rg"}
	set := make(map[string]bool, len(DefaultAllowlist))
	for _, b := range DefaultAllowlist {
		set[b] = true
	}
	for _, b := range must {
		if !set[b] {
			t.Fatalf("DefaultAllowlist missing multi-ecosystem binary %q", b)
		}
	}
	// Guard against accidental shell/network free-for-all.
	forbid := []string{"bash", "sh", "zsh", "curl", "wget", "sudo"}
	for _, b := range forbid {
		if set[b] {
			t.Fatalf("DefaultAllowlist must not include dangerous binary %q", b)
		}
	}
}

func TestOpenAIToolsJSONHasNoLanguageBias(t *testing.T) {
	// Round-trip through the exact payload shape sent to the model.
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	raw, err := json.Marshal(reg.OpenAITools())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, p := range languageBiasPatterns {
		if p.re.MatchString(s) {
			t.Fatalf("OpenAITools() payload matches language bias %q", p.name)
		}
	}
}
