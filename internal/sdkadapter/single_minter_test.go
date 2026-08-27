package sdkadapter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A reference format with several implementations is a defect generator.
// The live example: three minters disagreed, one of them truncated the
// digest to 8 bytes, and every output reference the model was handed
// pointed at a key nothing had stored - deterministically, silently,
// for every task.
//
// So this guard is structural rather than behavioural. It parses every
// production Go file in the module and fails if any string literal
// outside the allowlist looks like it builds a "ref:…" reference.
// Comments are invisible to it (it walks the AST, not the bytes), so
// prose describing the format is free.
//
// Invariant: "A reference handed to the model resolves, or it is not
// handed to the model." Corollary: minting lives in exactly one
// function.

// theMinter is the only file permitted to build a reference string.
const theMinter = "internal/sdkadapter/ref.go"

// allowedOtherMinters are pre-existing, non-content-reference uses of
// the "ref:" prefix, each allowed for a stated reason. Do not extend
// this list to make a new minter pass - that is the exact regression
// this test exists to catch.
var allowedOtherMinters = map[string]string{
	// normalizeReference rewrites a reference that exceeds the
	// ledger's 256-byte column bound into "ref:sha256:<digest>". It
	// is a storage-bound guard, not a content minter, and it is
	// unreachable for canonical references, which are 75 bytes.
	"internal/ledger/memory.go": "ledger overlong-reference storage guard",
}

// buildsReference reports whether a string literal looks like it
// constructs a reference rather than merely describing one.
// Model-facing tool descriptions have to tell an agent what a
// "ref:<kind>:<digest>" handle is, and that prose is not a minter. The
// discriminator is whitespace: a reference is a single token, so every
// format string that builds one ("ref:%s:%x") is whitespace-free, while
// a sentence explaining the format is not.
func buildsReference(literal string) bool {
	if !strings.Contains(literal, "ref:") {
		return false
	}
	return !strings.ContainsAny(literal, " \t\n")
}

// isNestedCheckout reports whether dir is the root of a second
// checkout of this module - a git worktree (the agent harness puts them
// under .claude/worktrees) or a vendored clone. Such a tree holds a
// full copy of every file here, so walking into it reports the same
// code twice; worse, the copy's path is prefixed, so it matches no
// allowlist entry and every permitted site reads as a violation. A
// worktree root carries a .git entry - a file rather than a directory
// - which is what marks it.
func isNestedCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// TestReferenceHasSingleMinter walks every production Go file in the
// module and fails if any string literal outside the allowlist looks
// like it builds a "ref:…" reference.
func TestReferenceHasSingleMinter(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	minterHits := 0
	var offenders []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "vendor" || name == "testdata" {
				return fs.SkipDir
			}
			if path != root && isNestedCheckout(path) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil || !buildsReference(value) {
				return true
			}
			switch {
			case rel == theMinter:
				minterHits++
			case allowedOtherMinters[rel] != "":
				// Permitted for the reason recorded above.
			default:
				offenders = append(offenders, rel+":"+
					strconv.Itoa(fset.Position(lit.Pos()).Line)+" "+strconv.Quote(value))
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	// Without this the test goes vacuous the moment the minter is
	// renamed or moved: zero literals anywhere would read as "no
	// second minter" rather than "the guard stopped looking at
	// anything".
	if minterHits == 0 {
		t.Fatalf("found no reference literal in %s - the guard is no longer watching the minter", theMinter)
	}

	if len(offenders) > 0 {
		t.Fatalf("content references must be minted only by %s (see the invariant in its package doc); "+
			"these sites build one themselves:\n  %s", theMinter, strings.Join(offenders, "\n  "))
	}
}
