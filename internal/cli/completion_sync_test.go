package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// executeSwitchCommandAliases lists non-canonical aliases: "version" and
// "help" alone represent these groups in completionCommands.
var executeSwitchCommandAliases = map[string]bool{"--version": true, "-V": true, "--help": true, "-h": true}

// executeSwitchCommands parses root.go's actual source and extracts every
// case label of the `switch args[0]` inside Execute, excluding
// executeSwitchCommandAliases. It fails the test outright (via t.Fatal) if
// the switch can't be found unambiguously, rather than returning a
// possibly-empty result silently.
func executeSwitchCommands(t *testing.T) map[string]bool {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this test file's path")
	}
	rootGoPath := filepath.Join(filepath.Dir(thisFile), "root.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rootGoPath, nil, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile(%q) error = %v", rootGoPath, err)
	}

	found := map[string]bool{}
	switchesSeen := 0

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Execute" {
			return true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			// Only the switch keyed on args[0] is the dispatch table; a
			// future nested switch inside a case body would otherwise be
			// picked up too.
			tagIdx, ok := sw.Tag.(*ast.IndexExpr)
			if !ok {
				return true
			}
			ident, ok := tagIdx.X.(*ast.Ident)
			if !ok || ident.Name != "args" {
				return true
			}
			switchesSeen++
			collectExecuteSwitchCaseLabels(t, sw, found)
			return false
		})
		return false
	})

	if switchesSeen != 1 {
		t.Fatalf("expected exactly one args[0] switch in Execute, found %d -- test's switch-selection logic needs review", switchesSeen)
	}
	if len(found) == 0 {
		t.Fatal("parsed zero case labels from Execute's switch -- test itself is broken, not a real pass")
	}
	return found
}

// collectExecuteSwitchCaseLabels extracts every string-literal case label
// from sw into found, skipping executeSwitchCommandAliases. Only raw string
// literals are extracted: a case switched to a named constant (case
// commandLogin:) would be silently skipped here, and since it'd also be
// absent from completionCommands, the bidirectional check in the caller
// would vacuously pass instead of catching real drift. Every case in
// Execute's switch is a raw literal today; revisit this if that ever
// changes.
func collectExecuteSwitchCaseLabels(t *testing.T, sw *ast.SwitchStmt, found map[string]bool) {
	t.Helper()
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("strconv.Unquote(%q) error = %v", lit.Value, err)
			}
			if !executeSwitchCommandAliases[val] {
				found[val] = true
			}
		}
	}
}

// TestCompletionCommandsMatchesExecuteSwitch asserts completionCommands
// equals Execute's real dispatch switch as a set, so a future command added
// to that switch without a matching completionCommands entry fails
// immediately -- completionCommands has silently drifted from the switch
// twice already in this repo's history (login/logout and memory), both
// times caught only by manual review.
func TestCompletionCommandsMatchesExecuteSwitch(t *testing.T) {
	found := executeSwitchCommands(t)

	want := map[string]bool{}
	for _, cmd := range completionCommands {
		want[cmd] = true
	}

	for cmd := range found {
		if !want[cmd] {
			t.Errorf("Execute() dispatches %q but completionCommands is missing it", cmd)
		}
	}
	for cmd := range want {
		if !found[cmd] {
			t.Errorf("completionCommands has %q but Execute() has no matching case", cmd)
		}
	}
}
