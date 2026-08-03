package tools

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
)

// navTools fetches the three code-navigation tools from a registry.
func navTools(t *testing.T, reg *Registry) (*findReferencesTool, *listSymbolsTool, *goToDefinitionTool) {
	t.Helper()
	get := func(name string) Tool {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		return tool
	}
	fr, ok := get("find_references").(*findReferencesTool)
	if !ok {
		t.Fatal("unexpected find_references type")
	}
	ls, ok := get("list_symbols").(*listSymbolsTool)
	if !ok {
		t.Fatal("unexpected list_symbols type")
	}
	gd, ok := get("go_to_definition").(*goToDefinitionTool)
	if !ok {
		t.Fatal("unexpected go_to_definition type")
	}
	return fr, ls, gd
}

// TestCodeNavToolsShareOneAnalyzer pins D3: the registry constructs ONE
// analyzer and hands it to all three nav tools. Three instances would each
// keep their own snapshot, so the cache would buy nothing across tools and
// the memory cost would triple.
func TestCodeNavToolsShareOneAnalyzer(t *testing.T) {
	reg, _ := newCapRegistry(t, 0, 1, 8)
	fr, ls, gd := navTools(t, reg)

	analyzer, ok := fr.finder.(*codeintel.Analyzer)
	if !ok {
		t.Fatalf("find_references finder is %T, want the shared analyzer", fr.finder)
	}
	if ls.searcher != symbolSearcher(analyzer) {
		t.Error("list_symbols does not use the same analyzer instance as find_references")
	}
	if gd.resolver != definitionResolver(analyzer) {
		t.Error("go_to_definition does not use the same analyzer instance as find_references")
	}
	if ls.outline == nil {
		t.Error("list_symbols has no file-outline backend")
	}
}

// TestGoToDefinitionBudgetClampedToConfiguredCap pins that a configured result
// cap tightens the tool's self-truncation budget, so the loop never has to cut
// its JSON envelope.
func TestGoToDefinitionBudgetClampedToConfiguredCap(t *testing.T) {
	reg, _ := newCapRegistry(t, 2048, 1, 8)
	_, _, gd := navTools(t, reg)
	if gd.maxBytes != 2048 {
		t.Fatalf("go_to_definition budget = %d, want clamped to 2048", gd.maxBytes)
	}
	if got := gd.Capability(nil).MaxResultBytes; got != 2048 {
		t.Fatalf("Capability.MaxResultBytes = %d, want 2048", got)
	}
}

// TestListSymbolsBudgetClampedToConfiguredCap is the same pin for list_symbols.
func TestListSymbolsBudgetClampedToConfiguredCap(t *testing.T) {
	reg, _ := newCapRegistry(t, 2048, 1, 8)
	_, ls, _ := navTools(t, reg)
	if ls.maxBytes != 2048 {
		t.Fatalf("list_symbols budget = %d, want clamped to 2048", ls.maxBytes)
	}
	if got := ls.Capability(nil).MaxResultBytes; got != 2048 {
		t.Fatalf("Capability.MaxResultBytes = %d, want 2048", got)
	}
}

// TestCodeNavBudgetsUnclampedWithoutCap: no configured ceiling leaves the
// tools' own 100KB budget in place.
func TestCodeNavBudgetsUnclampedWithoutCap(t *testing.T) {
	reg, _ := newCapRegistry(t, 0, 1, 8)
	_, ls, gd := navTools(t, reg)
	if ls.maxBytes != 100_000 || gd.maxBytes != 100_000 {
		t.Fatalf("budgets = list_symbols %d, go_to_definition %d; want 100000 each", ls.maxBytes, gd.maxBytes)
	}
}

// TestListSymbolsRegisteredLimitDefault pins the documented default: the
// schema the model sees says "default 50" and the registry must register that
// same number. Documented defaults matching code is the regression class that
// find_references' limit-50 drift belonged to.
func TestListSymbolsRegisteredLimitDefault(t *testing.T) {
	reg, _ := newCapRegistry(t, 0, 1, 8)
	_, ls, _ := navTools(t, reg)
	if ls.limit != 50 {
		t.Fatalf("list_symbols registered limit = %d, want 50", ls.limit)
	}
	if codeintel.DefaultSymbolLimit != 50 {
		t.Fatalf("codeintel.DefaultSymbolLimit = %d, want 50 to match the documented default",
			codeintel.DefaultSymbolLimit)
	}
}

// TestCodeNavToolsAbsentWithoutWorkspace: advertised iff it can succeed.
func TestCodeNavToolsAbsentWithoutWorkspace(t *testing.T) {
	reg := NewDefaultRegistry(DefaultOptions{})
	for _, name := range []string{"find_references", "list_symbols", "go_to_definition"} {
		if _, ok := reg.Get(name); ok {
			t.Errorf("%s registered with no workspace", name)
		}
	}
}
