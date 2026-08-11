package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Tool OpenAI-facing text must stay project- and language-generic.
// mivia is a host coding agent used in any repo; the tool surface must not
// teach models that this product is Go-only (or any single ecosystem).
//
// Rule: .mivia/rules/60-tools-project-language-generic.md

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

func collectModelFacingToolText(t *testing.T, opts ...DefaultOptions) map[string]string {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultOptions{Workspace: ws}
	if len(opts) > 0 {
		o = opts[0]
		o.Workspace = ws
	}
	reg := NewDefaultRegistry(o)
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
	assertNoLanguageBias(t, texts)
}

// assertNoLanguageBias is the shared rule-60 gate for model-facing tool text.
// texts maps tool name -> flattened Description plus parameter descriptions,
// so a failure names the offending tool and pattern.
// TestToolSurfaceIsProjectAndLanguageGeneric runs the whole registry through
// it; conditionally registered tools (e.g. get_diagnostics) pin their own
// surface with it too.
func assertNoLanguageBias(t *testing.T, texts map[string]string) {
	t.Helper()
	var failures []string
	for name, text := range texts {
		for _, p := range languageBiasPatterns {
			if p.re.MatchString(text) {
				failures = append(failures, name+": model-facing text matches "+p.name)
			}
		}
	}
	if len(failures) > 0 {
		t.Fatalf("tool surface must be project/language-generic (see .mivia/rules/60-tools-project-language-generic.md):\n  %s",
			strings.Join(failures, "\n  "))
	}
}

func TestToolSurfacePreferFilesystemOverRunCommand(t *testing.T) {
	// run_command must present as last resort so agents do not shell for cat/ls/grep.
	texts := collectModelFacingToolText(t, DefaultOptions{RunAllowlist: []string{"echo", "make", "npm", "git"}})
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

// The run allowlist is no longer compiled in, so the rule-60 guarantee that
// mivia is not a Go-only host now rests on the shipped example config. Assert
// it there, or the guarantee silently moves to a file nothing checks.
func TestExampleAllowlistIsMultiEcosystem(t *testing.T) {
	body := exampleRunAllowlist(t)

	must := []string{"git", "make", "python3", "npm", "node", "cargo", "rg"}
	for _, b := range must {
		if !strings.Contains(body, `"`+b+`"`) {
			t.Errorf("example run_allowlist missing multi-ecosystem binary %q", b)
		}
	}
	// bash, sh, curl and wget are deliberately present for scripted builds.
	for _, b := range []string{"sudo", "zsh", "dash", "ksh", "tcsh", "csh", "fish"} {
		if strings.Contains(body, `"`+b+`"`) {
			t.Errorf("example run_allowlist must not include dangerous binary %q", b)
		}
	}
}

// No allowlist compiled in means an unconfigured workspace runs nothing and
// does not advertise run_command at all. That is the documented posture; assert
// it so it cannot regress into a built-in.
func TestUnconfiguredAllowlistRunsNothing(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	// With an empty allowlist, run_command must not be registered at all.
	if _, ok := reg.Get("run_command"); ok {
		t.Fatal("unconfigured workspace must not advertise run_command")
	}
}

// exampleRunAllowlist returns just the run_allowlist array body. Scanning the
// whole file would match commented guidance (run_blocklist names "sudo").
func exampleRunAllowlist(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".mivia", "mivia.toml.example"))
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	body := string(data)
	start := strings.Index(body, "run_allowlist = [")
	if start < 0 {
		t.Fatal("example config has no run_allowlist")
	}
	end := strings.Index(body[start:], "\n]")
	if end < 0 {
		t.Fatal("unterminated run_allowlist in example config")
	}
	return body[start : start+end]
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

// get_diagnostics is conditionally registered: it exists only when a
// workspace configures [tools] diagnostics_command whose argv[0] is on the
// run_command allowlist. The whole-surface test above therefore never sees it
// in an unconfigured registry, so pin its surface here with the tool actually
// registered (rule 60).
func TestGetDiagnosticsSurfaceIsProjectAndLanguageGeneric(t *testing.T) {
	prog := onPathDiagnosticsProgram(t)
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:          ws,
		RunAllowlist:       []string{prog},
		DiagnosticsCommand: []string{prog},
	})
	tool, ok := reg.Get(GetDiagnosticsToolName)
	if !ok {
		t.Fatalf("get_diagnostics must register when DiagnosticsCommand=%q is on the run allowlist", prog)
	}

	// The model-facing surface: Description plus flattened parameter schemas.
	desc := tool.Description()
	text := desc + "\n" + strings.Join(flattenSchemaDescriptions(tool.Parameters()), "\n")
	assertNoLanguageBias(t, map[string]string{GetDiagnosticsToolName: text})

	// Rule 60 point 5: never this product's path or module in a Description().
	for _, product := range []string{"cmd/mivia", "github.com/MiviaLabs", "mivia-agent"} {
		if strings.Contains(desc, product) {
			t.Errorf("get_diagnostics Description must not name product path/module %q, got %q", product, desc)
		}
	}
	// Rule 60 point 2: examples span multiple ecosystems, never a single one.
	if ecosystemsNamed(desc) < 2 {
		t.Errorf("get_diagnostics Description must name multi-ecosystem examples, got %q", desc)
	}
}

// onPathDiagnosticsProgram returns a bare program name that resolves on PATH,
// which is the registration precondition for get_diagnostics (the surface
// test never executes the command). It skips when none of the candidates is
// available, mirroring requirePOSIXDiagnostics's portability stance.
func onPathDiagnosticsProgram(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"sh", "bash", "go", "python3", "python", "node", "npm", "git", "make"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	t.Skip("no on-PATH bare program available to register get_diagnostics")
	return ""
}

// multiEcosystemExamplePatterns match whole-word ecosystem example commands
// in Description() prose. Word boundaries keep "cmake" or "makefile" from
// ever counting as "make", so each match is a genuinely distinct ecosystem.
var multiEcosystemExamplePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"make", regexp.MustCompile(`(?i)\bmake\b`)},
	{"npm", regexp.MustCompile(`(?i)\bnpm\b`)},
	{"pytest", regexp.MustCompile(`(?i)\bpytest\b`)},
	{"cargo", regexp.MustCompile(`(?i)\bcargo\b`)},
	{"mvn", regexp.MustCompile(`(?i)\bmvn\b`)},
	{"gradle", regexp.MustCompile(`(?i)\bgradle\b`)},
	{"rake", regexp.MustCompile(`(?i)\brake\b`)},
	{"dotnet", regexp.MustCompile(`(?i)\bdotnet\b`)},
	{"cmake", regexp.MustCompile(`(?i)\bcmake\b`)},
	{"flutter", regexp.MustCompile(`(?i)\bflutter\b`)},
}

// ecosystemsNamed returns how many distinct ecosystem example markers appear
// in the text. One marker is a single-ecosystem example; the rule requires
// multiple.
func ecosystemsNamed(text string) int {
	count := 0
	for _, p := range multiEcosystemExamplePatterns {
		if p.re.MatchString(text) {
			count++
		}
	}
	return count
}
