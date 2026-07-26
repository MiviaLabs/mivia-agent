package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// This package test locks the *product repo's* .ai/agent-prompt.md contract:
// orientation for agents working on mivia-itself — not a living status dump.
//
// See docs/development/agent-self-prompt.md

var livingStateSmells = []struct {
	name string
	re   *regexp.Regexp
}{
	{"test count in parens", regexp.MustCompile(`\(\d+\+?\s*tests?\)`)},
	{"NEW feature banner", regexp.MustCompile(`(?m)^#{1,3}\s*.*\bNEW\b`)},
	{"Key Features section", regexp.MustCompile(`(?i)(?m)^#{1,3}\s*key features\b`)},
	{"Packages inventory section", regexp.MustCompile(`(?i)(?m)^#{1,3}\s*packages\b`)},
	{"What's implemented", regexp.MustCompile(`(?i)what'?s been implemented`)},
	{"Next priorities", regexp.MustCompile(`(?i)next priorities`)},
	{"All commits and what", regexp.MustCompile(`(?i)all commits and what`)},
	{"130+ tests style", regexp.MustCompile(`\d+\+\s*tests`)},
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/cli -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestRepoAgentPromptIsMetaOrientationNotState(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".ai", "agent-prompt.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (this repo must ship orientation prompt)", path, err)
	}
	content := string(data)
	lower := strings.ToLower(content)

	// Must establish self-work meta clearly.
	needles := []string{
		"working on yourself",
		"model-facing",
		"language-generic",
		"not", // used in "is not" / "do not" state rules
	}
	for _, n := range needles {
		if !strings.Contains(lower, n) {
			t.Fatalf(".ai/agent-prompt.md missing orientation cue %q", n)
		}
	}
	if !strings.Contains(lower, "discover") && !strings.Contains(lower, "tools") {
		t.Fatal(".ai/agent-prompt.md must tell the agent to discover state via tools")
	}

	var bad []string
	for _, s := range livingStateSmells {
		if s.re.MatchString(content) {
			bad = append(bad, s.name)
		}
	}
	if len(bad) > 0 {
		t.Fatalf(".ai/agent-prompt.md must not hold living project state (got smells: %s). Keep meta-orientation only.", strings.Join(bad, ", "))
	}
}
