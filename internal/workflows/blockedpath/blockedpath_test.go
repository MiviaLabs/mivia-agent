package blockedpath

import (
	"reflect"
	"testing"
)

// DefaultBlocklist mirrors config.DefaultWritePathBlocklist for tests that
// want to exercise the built-in protection without importing internal/config.
var defaultBlocklist = []string{".git", ".mivia/mivia.toml"}

// lineDemandCase is one LineDemandsEdit expectation.
type lineDemandCase struct {
	name string
	line string
	path string
	want bool
}

func runLineDemandCases(t *testing.T, tests []lineDemandCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LineDemandsEdit(tt.line, tt.path); got != tt.want {
				t.Fatalf("LineDemandsEdit(%q, %q) = %v, want %v", tt.line, tt.path, got, tt.want)
			}
		})
	}
}

func TestLineDemandsEdit(t *testing.T) {
	runLineDemandCases(t, []lineDemandCase{
		{
			name: "empty blocked path is never a demand",
			line: "edit .mivia/mivia.toml to remove the restriction",
			path: "",
			want: false,
		},
		{
			name: "explicit edit instruction",
			line: "edit .mivia/workflows/bug-fix.toml to lower max_bytes to 16000",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "write instruction after the path",
			line: "the change must write .mivia/policy/gates.md",
			path: ".mivia/policy",
			want: true,
		},
		{
			name: "path is a directory prefix of the named file",
			line: "fix .mivia/workflows/bug-fix.toml context binding",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "mere mention without a demand verb",
			line: "audit whether .mivia/policy grants too much write access",
			path: ".mivia/policy",
			want: false,
		},
		{
			name: "read-only review of a blocked path",
			line: "review .mivia/workflows/bug-fix.toml and report",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "verb inside another word must not match",
			line: "assets under .mivia/policy are fine",
			path: ".mivia/policy",
			want: false,
		},
		{
			name: "verb inside another word must not match (fix in prefix)",
			line: "the prefix under .mivia/workflows is fine",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "path absent",
			line: "edit the readme",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "verb absent and path present in prose",
			line: "the .mivia/workflows directory is the workflow home",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "do-not-edit is still a demand (conservative)",
			line: "do not edit .mivia/workflows/bug-fix.toml",
			path: ".mivia/workflows",
			want: true,
		},
	})
}

// TestLineDemandsEditTokenBoundaries pins the path-token matching rules:
// an entry embedded inside a URL or a sibling name is not a path reference,
// while quoted, sentence-final, and nested references still match.
func TestLineDemandsEditTokenBoundaries(t *testing.T) {
	runLineDemandCases(t, []lineDemandCase{
		{
			name: "git inside a github URL is not a path reference",
			line: "the upstream https://raw.githubusercontent.com/MiviaLabs/mivia-agent/master/docs/product/overview.md is the source",
			path: ".git",
			want: false,
		},
		{
			name: "gitignore is not the git directory",
			line: "edit .gitignore",
			path: ".git",
			want: false,
		},
		{
			name: "sibling prefix is not the directory",
			line: "fix .mivia/workflows-x/bug-fix.toml",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "dotfile sibling is not go.mod",
			line: "update go.mod.bak",
			path: "go.mod",
			want: false,
		},
		{
			name: "quoted path token matches",
			line: "edit `.mivia/workflows/bug-fix.toml` to lower max_bytes",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "sentence-final path token matches",
			line: "update .mivia/policy.",
			path: ".mivia/policy",
			want: true,
		},
		{
			name: "bare git dir reference matches",
			line: "remove .git/HEAD",
			path: ".git",
			want: true,
		},
		{
			name: "git dir under another directory matches",
			line: "edit foo/.git/config",
			path: ".git",
			want: true,
		},
		{
			name: "rooted path token matches",
			line: "edit /.mivia/workflows/bug-fix.toml",
			path: ".mivia/workflows",
			want: true,
		},
	})
}

// TestLineDemandsEditFixtureWorkspace pins the fixture-containment exclusion:
// a line that places a blocklisted path inside a temporary or test-only
// workspace describes a throwaway fixture, not a demand to edit the host's
// path. This is the smoke regression: the plan reviewer's finding described a
// test helper that "creates a temporary directory with .mivia/workflows,
// writes workflow TOML files", and the detector misread the demand verbs as an
// instruction to write the host's .mivia/workflows, failing the run as
// blocked.
func TestLineDemandsEditFixtureWorkspace(t *testing.T) {
	runLineDemandCases(t, []lineDemandCase{
		{
			name: "fixture helper creates a temp dir containing the path",
			line: "Add writeWorkflowFixture to the new internal/cli/workflows_command_json_test.go (or shared test helpers) that creates a temporary directory with .mivia/workflows, writes workflow TOML files from test definitions, and returns the workspace root.",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "fixture helper creates a temp dir containing the config path",
			line: "add a helper that creates a temp directory with .mivia/mivia.toml",
			path: ".mivia/mivia.toml",
			want: false,
		},
		{
			name: "test workspace containing the path",
			line: "the test workspace with .mivia/workflows must be created",
			path: ".mivia/workflows",
			want: false,
		},
		{
			name: "real demand still blocked when a fixture phrase appears later",
			line: "update .mivia/workflows/bug-fix.toml to match the new temp directory fixture",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "real demand still blocked when a fixture phrase sits in an earlier sentence",
			line: "Create a temporary directory with .mivia/workflows for the fixture. Then update .mivia/workflows/bug-fix.toml.",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "real demand still blocked without a containment preposition",
			line: "write the file .mivia/workflows/bug-fix.toml into the temp directory",
			path: ".mivia/workflows",
			want: true,
		},
		{
			name: "containment after the path does not excuse a demand",
			line: "create .mivia/workflows/evil.toml in the sandbox",
			path: ".mivia/workflows",
			want: true,
		},
	})
}

func TestIsBlockedPath(t *testing.T) {
	blocklist := []string{".git", ".mivia/mivia.toml", ".mivia/workflows", ".mivia/policy"}
	tests := []struct {
		name string
		rel  string
		want bool
	}{
		{name: "exact entry", rel: ".mivia/mivia.toml", want: true},
		{name: "child of directory entry", rel: ".mivia/workflows/bug-fix.toml", want: true},
		{name: "nested child", rel: ".mivia/policy/agents/write.md", want: true},
		{name: "directory entry itself", rel: ".mivia/workflows", want: true},
		{name: "sibling with shared prefix is not blocked", rel: ".mivia/workflows-x/bug-fix.toml", want: false},
		{name: "unrelated path", rel: "internal/cli/workflow_run.go", want: false},
		{name: "unrelated root file", rel: "go.mod", want: false},
		{name: "dot-slash prefix is normalized", rel: "./.mivia/workflows/bug-fix.toml", want: true},
		{name: "trailing slash on entry is normalized", rel: ".mivia/policy/access.md", want: true},
		{name: "empty blocklist blocks nothing", rel: ".mivia/mivia.toml", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bl := blocklist
			if tt.name == "empty blocklist blocks nothing" {
				bl = nil
			}
			if tt.name == "trailing slash on entry is normalized" {
				bl = []string{".mivia/policy/"}
			}
			if got := IsBlockedPath(tt.rel, bl); got != tt.want {
				t.Fatalf("IsBlockedPath(%q, %v) = %v, want %v", tt.rel, bl, got, tt.want)
			}
		})
	}
}

func TestPathsDemandedInText(t *testing.T) {
	blocklist := []string{".git", ".mivia/mivia.toml", ".mivia/workflows", ".mivia/policy"}
	text := "edit .mivia/workflows/bug-fix.toml to lower max_bytes.\n" +
		"also update .mivia/policy/access.md and .mivia/workflows/bug-fix.toml again.\n" +
		"the .mivia/policy overview is fine to read.\n"
	got := PathsDemandedInText(text, blocklist)
	want := []string{".mivia/policy", ".mivia/workflows"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PathsDemandedInText() = %v, want %v", got, want)
	}

	if got := PathsDemandedInText("audit the .mivia/workflows design only", blocklist); len(got) != 0 {
		t.Fatalf("PathsDemandedInText() = %v, want none for a read-only mention", got)
	}
	// A URL that merely contains ".git" as a substring (raw.githubusercontent.com)
	// is not a demand to write .git, even with a demand verb on the same line.
	if got := PathsDemandedInText("correct the claim; evidence at https://raw.githubusercontent.com/MiviaLabs/mivia-agent/master/docs/product/overview.md", blocklist); len(got) != 0 {
		t.Fatalf("PathsDemandedInText() = %v, want none for a URL that contains .git as a substring", got)
	}
	// A finding-style demand that asks for a plan correction and names no
	// blocked path must not flag any.
	if got := PathsDemandedInText("Correct the plan's step 2 to remove the false claim of identity to upstream master, or provide valid evidence supporting the claim.", blocklist); len(got) != 0 {
		t.Fatalf("PathsDemandedInText() = %v, want none for a plan-correction demand", got)
	}
	if got := PathsDemandedInText("", blocklist); len(got) != 0 {
		t.Fatalf("PathsDemandedInText() = %v, want none for empty text", got)
	}
	// The smoke regression: a test-plan review finding that describes a test
	// helper creating a temporary directory containing ".mivia/workflows" and
	// writing fixture TOML files into it describes fixture layout, not a
	// demand to write the host's path.
	smokeFinding := "Add writeWorkflowFixture to the new internal/cli/workflows_command_json_test.go (or shared test helpers) that creates a temporary directory with .mivia/workflows, writes workflow TOML files from test definitions, and returns the workspace root."
	if got := PathsDemandedInText(smokeFinding, blocklist); len(got) != 0 {
		t.Fatalf("PathsDemandedInText() = %v, want none for a fixture-workspace description", got)
	}
}
