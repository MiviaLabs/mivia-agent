package delivery_test

// The no-merge-authority contract for the engine stack driver: ONLY the
// delivery package itself (the merge action in prmerge.go) and the CLI
// driver (internal/cli/chat, merge_policy=auto) may call the PR merge
// action. The engine (internal/workflows/localengine) must never merge; it
// only delivers and observes merges through delivery.MergeProbe.
//
// The test resolves the merge symbols by type identity through go/packages,
// so a rename cannot erode it, and fails on any new caller package (AR-3,
// architecture review of the stack-driver conformance plan).

import (
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

const (
	pkgDelivery = "github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	pkgCLIDrive = "github.com/MiviaLabs/mivia-agent/internal/cli/workflow"
)

// mergeSymbols returns the type-identity object of the merge action:
// delivery.MergePullRequest, the only function that lands a PR.
func mergeSymbols(t *testing.T, deliveryPkg *packages.Package) []types.Object {
	t.Helper()
	fn := deliveryPkg.Types.Scope().Lookup("MergePullRequest")
	if fn == nil {
		t.Fatal("delivery.MergePullRequest not found; update the merge-caller contract")
	}
	return []types.Object{fn}
}

func TestMergeActionCallerSet(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir:   root,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, "./internal/...", "./cmd/...")
	if err != nil {
		t.Fatalf("load packages: %v", err)
	}
	var deliveryPkg *packages.Package
	byPath := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.PkgPath == pkgDelivery {
			deliveryPkg = p
		}
		byPath[p.PkgPath] = p
	}
	if deliveryPkg == nil {
		t.Fatal("delivery package not loaded")
	}
	targets := mergeSymbols(t, deliveryPkg)
	isTarget := map[types.Object]bool{}
	for _, o := range targets {
		isTarget[o] = true
	}

	callers := map[string]string{} // pkg path -> first symbol name used
	for _, p := range byPath {
		for id, obj := range p.TypesInfo.Uses {
			if !isTarget[obj] {
				continue
			}
			if _, seen := callers[p.PkgPath]; !seen {
				callers[p.PkgPath] = id.Name
			}
		}
	}
	allowed := map[string]bool{pkgDelivery: true, pkgCLIDrive: true}
	for path, sym := range callers {
		if !allowed[path] {
			t.Errorf("merge symbol %q is used by %q, which is not an allowed merge authority (allowed: delivery, internal/cli/workflow); the engine stack driver must stay observe-only", sym, path)
		}
	}
	if callers[pkgCLIDrive] == "" {
		t.Log("note: the CLI driver currently shows no direct merge-symbol use; if the merge path moved, revisit this contract")
	}
}
