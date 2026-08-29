package subagents

// GATE: request-field parity between the two runtime.Handler implementations.
//
// The class this catches, named by mechanism: a request-scoped contract field
// read by one implementation of an interface and silently dropped by its
// sibling. It is this repository's most expensive recurring defect - seven
// bugs of that shape shipped together in provider.Completer, and two more on
// this very pair (req.OutputSchema and req.DisableProviderReplay, both honored
// by MultiStepHandler and ignored by OneShotHandler). dispatch_tasks told the
// model its output_schema was "Validated before the task completes" while the
// agent-less path validated nothing, and a caller asking for exactly one
// provider request got five. No gate noticed either.
//
// The check is a read-set comparison over the AST: for each handler family,
// which runtime.Request fields does its non-test source actually reference?
// Any field one family reads and the other does not must be declared in
// .mivia/policy/handler-field-parity.json with a reason. A NEW asymmetry -
// including a new Request field wired into one handler only - fails here.
//
// What this gate CANNOT catch, stated so nobody trusts it further than it
// goes: it sees that a field is read, not that it is honored correctly. A
// handler that reads a field and then ignores its value passes. It also
// attributes reads by filename family (multi_step*.go, oneshot*.go), which is
// this package's actual layout but is a convention, not a compiler rule; a
// helper shared between families would be attributed to neither.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type handlerParityPolicy struct {
	Families        map[string]string `json:"families"`
	KnownAsymmetric map[string]string `json:"knownAsymmetric"`
}

func loadHandlerParityPolicy(t *testing.T) handlerParityPolicy {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".mivia", "policy", "handler-field-parity.json"))
	if err != nil {
		t.Fatalf("read parity policy: %v", err)
	}
	var policy handlerParityPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode parity policy: %v", err)
	}
	if len(policy.Families) < 2 {
		t.Fatalf("policy must name at least two handler families, got %d", len(policy.Families))
	}
	return policy
}

// requestFieldReads returns the exported runtime.Request fields referenced as
// `req.Field` in the family's non-test source.
func requestFieldReads(t *testing.T, family string) map[string]bool {
	t.Helper()
	matches, err := filepath.Glob(family + "*.go")
	if err != nil {
		t.Fatalf("glob %q: %v", family, err)
	}
	reads := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "req" {
				return true
			}
			if name := sel.Sel.Name; name != "" && ast.IsExported(name) {
				reads[name] = true
			}
			return true
		})
	}
	if len(reads) == 0 {
		t.Fatalf("family %q referenced no request fields; the glob or the layout changed", family)
	}
	return reads
}

func TestHandlerRequestFieldParity(t *testing.T) {
	policy := loadHandlerParityPolicy(t)

	families := make([]string, 0, len(policy.Families))
	for family := range policy.Families {
		families = append(families, family)
	}
	sort.Strings(families)

	readsByFamily := map[string]map[string]bool{}
	for _, family := range families {
		readsByFamily[family] = requestFieldReads(t, family)
	}

	for _, family := range families {
		for _, other := range families {
			if family == other {
				continue
			}
			for field := range readsByFamily[family] {
				if readsByFamily[other][field] {
					continue
				}
				if reason, declared := policy.KnownAsymmetric[field]; declared {
					if strings.TrimSpace(reason) == "" {
						t.Errorf("field %q is declared asymmetric with an empty reason", field)
					}
					continue
				}
				t.Errorf("runtime.Request.%s is read by %s but not by %s.\n"+
					"  A contract honored by one handler and dropped by its sibling is how\n"+
					"  OutputSchema and DisableProviderReplay were lost on the agent-less path.\n"+
					"  Either honor it in %s, or declare it in\n"+
					"  .mivia/policy/handler-field-parity.json with the reason it does not apply.",
					field, policy.Families[family], policy.Families[other], policy.Families[other])
			}
		}
	}
}

// A declared exemption that is no longer asymmetric is stale: it records a gap
// that has been closed, and left in place it would hide the field's next
// regression.
func TestHandlerParityPolicyHasNoStaleExemptions(t *testing.T) {
	policy := loadHandlerParityPolicy(t)

	readsByFamily := map[string]map[string]bool{}
	for family := range policy.Families {
		readsByFamily[family] = requestFieldReads(t, family)
	}

	for field := range policy.KnownAsymmetric {
		readCount := 0
		for _, reads := range readsByFamily {
			if reads[field] {
				readCount++
			}
		}
		if readCount == 0 {
			t.Errorf("policy exempts %q but no handler reads it; drop the entry", field)
		}
		if readCount == len(readsByFamily) {
			t.Errorf("policy exempts %q but every handler now reads it; the gap is closed, so drop the entry and let the gate protect it", field)
		}
	}
}
