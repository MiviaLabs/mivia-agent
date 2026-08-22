package clichat

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// Compiled-in default prompts are the fallback for *any* workspace that lacks
// a file-backed agent definition under .mivia/agents/. They must stay
// project- and language-generic.
//
// Rule 60: tools, project and language generic.

var promptLanguageBias = []struct {
	name string
	re   *regexp.Regexp
}{
	{"go test exclusive stack", regexp.MustCompile(`(?i)go test \./\.\.\.`)},
	{"go build -o mivia", regexp.MustCompile(`(?i)go build\s+-o\s+mivia`)},
	{"go vet", regexp.MustCompile(`(?i)\bgo vet\b`)},
	{"go test -race", regexp.MustCompile(`(?i)go test\s+-race`)},
	{"github.com/MiviaLabs module", regexp.MustCompile(`github\.com/MiviaLabs`)},
	{"cmd/mivia only build path", regexp.MustCompile(`cmd/mivia`)},
	{"Allowed scopes list as universal", regexp.MustCompile(`(?i)Allowed scopes:\s*cli,\s*agent`)},
}

func TestDefaultAgentPromptIsLanguageGeneric(t *testing.T) {
	p := buildAgentPrompt(config.SubagentConfig{})
	for _, b := range promptLanguageBias {
		if b.re.MatchString(p) {
			t.Fatalf("buildAgentPrompt matches language/product bias %q - keep fallback generic; put repo-specific knowledge in .mivia/agents/*.toml only", b.name)
		}
	}
	// Must teach discovery + generic tool discipline.
	needles := []string{
		".mivia/agents/",
		"run_command",
		"workspace",
		"argv",
		"read_output",
		"ledger_read",
	}
	for _, n := range needles {
		if !strings.Contains(p, n) {
			t.Fatalf("buildAgentPrompt missing required guidance %q", n)
		}
	}
	// Prefer filesystem tools / last-resort run is required discipline.
	lower := strings.ToLower(p)
	if !strings.Contains(lower, "prefer") && !strings.Contains(lower, "discover") {
		t.Fatal("buildAgentPrompt should tell the model to prefer tools / discover project conventions")
	}
}

func TestDefaultSystemPromptIsNotProductSelfOnly(t *testing.T) {
	// Plain chat mode still names mivia, but must not claim the only job is
	// improving the mivia product binary.
	p := strings.ToLower(defaultSystemPrompt)
	if strings.Contains(p, "improve the mivia agent product itself") {
		t.Fatal("defaultSystemPrompt must not frame the agent as self-improvement-only")
	}
	if !strings.Contains(p, "mivia") {
		t.Fatal("defaultSystemPrompt should still identify as mivia")
	}
}

func TestDefaultAgentPromptDoesNotHardcodeSingleEcosystemVerify(t *testing.T) {
	// Single-ecosystem verify blocks in the *compiled* default teach the wrong
	// habits when mivia is used outside this monorepo.
	p := buildAgentPrompt(config.SubagentConfig{})
	if strings.Contains(p, "go test ./...") && !strings.Contains(strings.ToLower(p), "discover") {
		t.Fatal("do not hardcode go test as the universal verify command in buildAgentPrompt")
	}
	// Explicit multi-ecosystem or discovery language.
	lower := strings.ToLower(p)
	hasDiscovery := strings.Contains(lower, "discover") ||
		strings.Contains(lower, "project") ||
		strings.Contains(lower, "makefile") ||
		strings.Contains(lower, "package.json") ||
		strings.Contains(lower, "whatever the project")
	if !hasDiscovery {
		t.Fatal("buildAgentPrompt must tell the model to discover project-local verify/build conventions")
	}
}
