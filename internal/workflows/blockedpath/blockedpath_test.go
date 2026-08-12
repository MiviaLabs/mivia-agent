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
}
