package redact

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// credentialKeyword marks a regex literal that is trying to recognise a secret.
var credentialKeyword = regexp.MustCompile(`(?i)bearer|api[_-]?key|passwd|password|private key|sk-ant|ghp_|github_pat|xox[baprs]`)

// Four separate hardcoded pattern lists once existed in this repo. They drifted
// apart, over-redacted ordinary prose and missed credentials none of them named.
// They did not arrive in one commit - they grew one call site at a time, each
// addition looking locally reasonable.
//
// This walks the shipped sources and fails on any regexp literal that tries to
// recognise a credential outside this package. Redaction is configuration; a
// pattern in Go is a defect. See .mivia/rules/10-security-privacy.md.
// isNestedCheckout reports whether dir is the root of a second checkout of this
// module - a git worktree or a vendored clone - which carries a full copy of
// every file here. A worktree root is marked by a .git entry.
func isNestedCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func TestNoCompiledRedactionPatterns(t *testing.T) {
	root := repoRoot(t)
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", "vendor":
				return filepath.SkipDir
			}
			// A git worktree under .claude/worktrees is a second copy of this
			// module: walking in scans every file twice and reports the copy's
			// prefixed path as a fresh offender.
			if path != root && isNestedCheckout(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, filepath.Join("internal", "redact")) {
			return nil // this package is where patterns are allowed to be named
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "regexp.MustCompile") && !strings.Contains(line, "regexp.Compile") {
				continue
			}
			if credentialKeyword.MatchString(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) > 0 {
		t.Fatalf("credential patterns must come from [privacy] config, not Go source; found %d:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}
