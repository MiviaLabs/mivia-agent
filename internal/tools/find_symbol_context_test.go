package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// fakeSymbolContextResolver composes the existing fakeDefinitionResolver and
// fakeReferenceFinder (defined in go_to_definition_test.go and
// find_references_test.go) rather than declaring a third, parallel fake -
// symbolContextResolver is itself a superset of definitionResolver and
// referenceFinder, so the two already-shipped fakes satisfy it by embedding.
type fakeSymbolContextResolver struct {
	fakeDefinitionResolver
	fakeReferenceFinder
}

func execFindSymbolContext(t *testing.T, reg *Registry, argsJSON string) findSymbolContextOutput {
	t.Helper()
	raw, err := reg.Execute(context.Background(), "find_symbol_context", json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw=%s", err, raw)
	}
	return out
}

func execFindSymbolContextErr(t *testing.T, reg *Registry, argsJSON string) error {
	t.Helper()
	_, err := reg.Execute(context.Background(), "find_symbol_context", json.RawMessage(argsJSON))
	if err == nil {
		t.Fatalf("expected error, got none")
	}
	return err
}

// --- Wave 1: schema and config ---

func TestFindSymbolContextSchemaRejectsInvalidBounds(t *testing.T) {
	_, reg := setupWS(t)

	cases := []string{
		`{}`,                                    // missing symbol
		`{"symbol":""}`,                         // empty symbol
		`{"symbol":"Foo","max_references":0}`,   // below min
		`{"symbol":"Foo","max_references":101}`, // above max
		`{"symbol":"Foo","context_lines":-1}`,   // below min
		`{"symbol":"Foo","context_lines":11}`,   // above max
		`{"symbol":"Foo","mode":"list"}`,        // unknown field
		`{"symbol":"Foo","max_references":10,"context_lines":5,"x":1}`, // unknown field
	}
	for _, args := range cases {
		if err := execFindSymbolContextErr(t, reg, args); err == nil {
			t.Errorf("args=%s: expected rejection", args)
		}
	}
}

func TestFindSymbolContextConfigDefaultsAndBounds(t *testing.T) {
	_, reg := setupWS(t)

	// Symbol required; only bound checked here is that the tool is registered
	// and rejects an out-of-range explicit value, matching inspect_repository's
	// pattern of enforcing its own bounds independent of schema validation.
	if err := execFindSymbolContextErr(t, reg, `{"symbol":"Foo","max_references":1000}`); err == nil {
		t.Fatalf("expected rejection of out-of-range max_references")
	}
	if err := execFindSymbolContextErr(t, reg, `{"symbol":"Foo","context_lines":50}`); err == nil {
		t.Fatalf("expected rejection of out-of-range context_lines")
	}

	tool, ok := reg.Get("find_symbol_context")
	if !ok {
		t.Fatal("find_symbol_context not registered")
	}
	if _, ok := tool.(ResultBudgetTool); !ok {
		t.Fatal("find_symbol_context does not implement ResultBudgetTool")
	}
}

// --- Wave 2: composition ---

// TestFindSymbolContextComposesDefinitionAndReferences: named "...Definition
// AndReferences" rather than the plan follow-on section's literal
// "...DefinitionReferencesAndSymbols" - Analyzer.Symbols has no field in the
// documented output envelope (definition, references, reference_count,
// reference_truncated, symbol_available, error, provenance) to populate, and
// calling it here without consuming a result would be dead code. See
// symbolContextResolver's doc comment: it is deliberately a superset of the
// two interfaces this composition actually needs (definitionResolver,
// referenceFinder), not three.
func TestFindSymbolContextComposesDefinitionAndReferences(t *testing.T) {
	ws := navWorkspace(t)
	resolver := &fakeSymbolContextResolver{
		fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{
			Symbol: "pkg.Widget", Kind: codeintel.KindFunc, Package: "pkg",
			Path: ws.Abs + "/pkg/widget.go", Line: 10, EndLine: 14,
			Signature: "func Widget()", Source: "func Widget() {\n\treturn\n}",
		}},
		fakeReferenceFinder: fakeReferenceFinder{result: codeintel.Result{
			Symbol: "pkg.Widget",
			Locations: []codeintel.Location{
				{Path: ws.Abs + "/pkg/other.go", Line: 22, Symbol: "Widget", Role: codeintel.RoleCaller},
			},
			Complete: true,
		}},
	}
	tool := &findSymbolContextTool{ws: ws, resolver: resolver, maxBytes: 10000}
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"pkg.Widget"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if !out.SymbolAvailable || out.Error != "" {
		t.Fatalf("expected success, got %+v", out)
	}
	if out.Definition == nil {
		t.Fatal("expected a definition")
	}
	if out.Definition.Path != "pkg/widget.go" || out.Definition.Line != 10 || out.Definition.EndLine != 14 {
		t.Fatalf("definition = %+v", out.Definition)
	}
	if out.Definition.Signature != "func Widget()" {
		t.Fatalf("signature = %q", out.Definition.Signature)
	}
	if len(out.References) != 1 || out.References[0].Path != "pkg/other.go" || out.References[0].Line != 22 || out.References[0].Role != codeintel.RoleCaller {
		t.Fatalf("references = %+v", out.References)
	}
	if out.ReferenceCount != 1 || out.ReferenceTruncated {
		t.Fatalf("reference_count=%d reference_truncated=%v", out.ReferenceCount, out.ReferenceTruncated)
	}
	if resolver.fakeReferenceFinder.lastLimit < findSymbolContextMaxMaxReferences {
		t.Fatalf("References fetch limit=%d must exceed max_references bound so this tool's own sort-then-truncate decides survivors (AR-1), not Analyzer's own unsorted cap", resolver.fakeReferenceFinder.lastLimit)
	}
}

func TestFindSymbolContextReportsAnalyzerUnavailable(t *testing.T) {
	ws := navWorkspace(t)

	tool := &findSymbolContextTool{ws: ws, resolver: nil, maxBytes: 10000}
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget"}`))
	if err != nil {
		t.Fatalf("availability must be output, not a call error: %v", err)
	}
	var out findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.SymbolAvailable {
		t.Fatalf("expected symbol_available=false with no resolver, got %+v", out)
	}
	if !strings.Contains(out.Error, "no analyzer available") {
		t.Fatalf("error = %q", out.Error)
	}
	if out.Definition != nil || len(out.References) != 0 {
		t.Fatalf("expected empty definition/references, got %+v", out)
	}

	unavailable := &findSymbolContextTool{
		ws: ws,
		resolver: &fakeSymbolContextResolver{
			fakeDefinitionResolver: fakeDefinitionResolver{err: fmt.Errorf("analysis unavailable: %w", codeintel.ErrUnavailable)},
		},
		maxBytes: 10000,
	}
	raw2, err := unavailable.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget"}`))
	if err != nil {
		t.Fatalf("availability must be output, not a call error: %v", err)
	}
	var out2 findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw2), &out2); err != nil {
		t.Fatal(err)
	}
	if out2.SymbolAvailable {
		t.Fatalf("expected symbol_available=false when the analyzer reports ErrUnavailable, got %+v", out2)
	}

	// A resolved-but-not-found symbol is a different case: the analyzer
	// itself IS available, only this particular symbol was not resolved.
	notFound := &findSymbolContextTool{
		ws: ws,
		resolver: &fakeSymbolContextResolver{
			fakeDefinitionResolver: fakeDefinitionResolver{err: fmt.Errorf(`symbol "Missing" not found in workspace packages`)},
		},
		maxBytes: 10000,
	}
	raw3, err := notFound.Execute(context.Background(), json.RawMessage(`{"symbol":"Missing"}`))
	if err != nil {
		t.Fatalf("not-found must be output, not a call error: %v", err)
	}
	var out3 findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw3), &out3); err != nil {
		t.Fatal(err)
	}
	if !out3.SymbolAvailable {
		t.Fatalf("expected symbol_available=true when the analyzer works but this symbol was not found, got %+v", out3)
	}
	if !strings.Contains(out3.Error, "not found") {
		t.Fatalf("error = %q", out3.Error)
	}
}

func TestFindSymbolContextContextWindowsAreExact(t *testing.T) {
	ws := navWorkspace(t)
	src := "func Widget() {\n\tline2\n\tline3\n\tline4\n\treturn\n}"

	// context_lines below the full decl length: exact prefix, truncated.
	trimmed := &findSymbolContextTool{
		ws: ws,
		resolver: &fakeSymbolContextResolver{fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{
			Symbol: "Widget", Kind: codeintel.KindFunc, Path: ws.Abs + "/w.go", Line: 1, EndLine: 6,
			Signature: "func Widget()", Source: src,
		}}},
		maxBytes: 10000,
	}
	out := execToolJSON[findSymbolContextOutput](t, trimmed, `{"symbol":"Widget","context_lines":3}`)
	wantLines := strings.Join(strings.Split(src, "\n")[:3], "\n")
	if out.Definition == nil || out.Definition.Source != wantLines {
		t.Fatalf("source = %q, want %q", out.Definition.Source, wantLines)
	}
	if !out.Definition.SourceTruncated {
		t.Fatal("expected source_truncated=true")
	}

	// context_lines covering the whole decl: no truncation.
	full := execToolJSON[findSymbolContextOutput](t, trimmed, `{"symbol":"Widget","context_lines":10}`)
	if full.Definition.Source != src {
		t.Fatalf("source = %q, want full %q", full.Definition.Source, src)
	}
	if full.Definition.SourceTruncated {
		t.Fatal("expected source_truncated=false when the window covers the whole declaration")
	}

	// context_lines=0: an explicit choice to see no source, not a truncation.
	zero := execToolJSON[findSymbolContextOutput](t, trimmed, `{"symbol":"Widget","context_lines":0}`)
	if zero.Definition.Source != "" {
		t.Fatalf("source = %q, want empty", zero.Definition.Source)
	}
	if zero.Definition.SourceTruncated {
		t.Fatal("context_lines=0 is a choice, not a truncation")
	}
}

// execToolJSON runs a tool directly (bypassing the registry, matching
// go_to_definition_test.go's and find_references_test.go's style of testing
// against fakes) and decodes its JSON output into T.
func execToolJSON[T any](t *testing.T, tool Tool, argsJSON string) T {
	t.Helper()
	raw, err := tool.Execute(context.Background(), json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	return out
}

func manyLocations(n int) []codeintel.Location {
	out := make([]codeintel.Location, n)
	for i := 0; i < n; i++ {
		out[i] = codeintel.Location{
			Path: fmt.Sprintf("f%03d.go", i), Line: 1, Symbol: "Widget", Role: codeintel.RoleCaller,
		}
	}
	return out
}

func TestFindSymbolContextReportsReferenceAndByteTruncationHonestly(t *testing.T) {
	ws := navWorkspace(t)
	resolver := &fakeSymbolContextResolver{
		fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{Symbol: "Widget", Kind: codeintel.KindFunc, Path: ws.Abs + "/w.go", Line: 1, EndLine: 1, Signature: "func Widget()"}},
		fakeReferenceFinder:    fakeReferenceFinder{result: codeintel.Result{Symbol: "Widget", Locations: manyLocations(30), Complete: true}},
	}

	// max_references caps the count even with a generous byte budget.
	tool := &findSymbolContextTool{ws: ws, resolver: resolver, maxBytes: 100000}
	out := execToolJSON[findSymbolContextOutput](t, tool, `{"symbol":"Widget","max_references":5}`)
	if out.ReferenceCount != 5 || len(out.References) != 5 {
		t.Fatalf("reference_count=%d len(references)=%d, want 5", out.ReferenceCount, len(out.References))
	}
	if !out.ReferenceTruncated {
		t.Fatal("expected reference_truncated=true when more references existed than max_references")
	}

	// A byte budget too small for the full requested set truncates further
	// and still reports truncated=true honestly.
	tight := &findSymbolContextTool{ws: ws, resolver: resolver, maxBytes: 300}
	raw, err := tight.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget","max_references":30}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > 300 {
		t.Fatalf("output len=%d exceeds configured budget 300", len(raw))
	}
	var tightOut findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw), &tightOut); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if !tightOut.ReferenceTruncated {
		t.Fatal("expected reference_truncated=true under a tight byte budget")
	}
	if tightOut.ReferenceCount != len(tightOut.References) {
		t.Fatalf("reference_count=%d != len(references)=%d", tightOut.ReferenceCount, len(tightOut.References))
	}

	// Untruncated case: reference_truncated must be false, not just omitted.
	small := &fakeSymbolContextResolver{
		fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{Symbol: "Widget", Kind: codeintel.KindFunc, Path: ws.Abs + "/w.go", Line: 1, EndLine: 1, Signature: "func Widget()"}},
		fakeReferenceFinder:    fakeReferenceFinder{result: codeintel.Result{Symbol: "Widget", Locations: manyLocations(2), Complete: true}},
	}
	untruncated := execToolJSON[findSymbolContextOutput](t, &findSymbolContextTool{ws: ws, resolver: small, maxBytes: 100000}, `{"symbol":"Widget","max_references":20}`)
	if untruncated.ReferenceTruncated {
		t.Fatalf("did not expect truncation: %+v", untruncated)
	}
}

func TestFindSymbolContextSortsReferencesBeforeTruncating(t *testing.T) {
	ws := navWorkspace(t)
	// Deliberately unsorted: Analyzer.References's own order is unspecified
	// (AR-1), so the tool must sort by (path, line, role) itself before
	// max_references cuts the list - otherwise which references survive
	// would depend on map iteration order, not on workspace state.
	unsorted := []codeintel.Location{
		{Path: "z.go", Line: 9, Symbol: "Widget", Role: codeintel.RoleCaller},
		{Path: "a.go", Line: 5, Symbol: "Widget", Role: codeintel.RoleCaller},
		{Path: "a.go", Line: 1, Symbol: "Widget", Role: codeintel.RoleCaller},
		{Path: "m.go", Line: 3, Symbol: "Widget", Role: codeintel.RoleCaller},
	}
	resolver := &fakeSymbolContextResolver{
		fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{Symbol: "Widget", Kind: codeintel.KindFunc, Path: ws.Abs + "/w.go", Line: 1, EndLine: 1, Signature: "func Widget()"}},
		fakeReferenceFinder:    fakeReferenceFinder{result: codeintel.Result{Symbol: "Widget", Locations: unsorted, Complete: true}},
	}
	tool := &findSymbolContextTool{ws: ws, resolver: resolver, maxBytes: 10000}
	out := execToolJSON[findSymbolContextOutput](t, tool, `{"symbol":"Widget","max_references":2}`)
	if len(out.References) != 2 {
		t.Fatalf("references=%+v, want 2", out.References)
	}
	if out.References[0].Path != "a.go" || out.References[0].Line != 1 {
		t.Fatalf("references[0]=%+v, want a.go:1 (lowest sort key survives truncation)", out.References[0])
	}
	if out.References[1].Path != "a.go" || out.References[1].Line != 5 {
		t.Fatalf("references[1]=%+v, want a.go:5", out.References[1])
	}
	// Determinism: re-running against identical (unsorted) analyzer output
	// must yield the same survivors in the same order every time.
	out2 := execToolJSON[findSymbolContextOutput](t, tool, `{"symbol":"Widget","max_references":2}`)
	if len(out2.References) != 2 || out2.References[0] != out.References[0] || out2.References[1] != out.References[1] {
		t.Fatalf("nondeterministic truncation: %+v vs %+v", out.References, out2.References)
	}
}

// --- Wave 3: registry, budget, capability, provenance ---

// TestDefaultRegistryRegistersFindSymbolContextSharingAnalyzer asserts, at
// the concrete-field level (this test lives in package tools, so unexported
// fields are visible), that find_symbol_context resolves against the exact
// same *codeintel.Analyzer instance registerCodeNavTools hands to
// go_to_definition - not a second, independently constructed one. A second
// analyzer would pay its own full packages.Load and defeat the whole point
// of the shared cache (Design, plan 66 follow-on #1).
func TestDefaultRegistryRegistersFindSymbolContextSharingAnalyzer(t *testing.T) {
	_, reg := setupWS(t)

	goDefTool, ok := reg.Get("go_to_definition")
	if !ok {
		t.Fatal("go_to_definition not registered")
	}
	goDef, ok := goDefTool.(*goToDefinitionTool)
	if !ok {
		t.Fatal("go_to_definition is not *goToDefinitionTool")
	}

	symCtxTool, ok := reg.Get("find_symbol_context")
	if !ok {
		t.Fatal("find_symbol_context not registered")
	}
	symCtx, ok := symCtxTool.(*findSymbolContextTool)
	if !ok {
		t.Fatal("find_symbol_context is not *findSymbolContextTool")
	}

	if any(goDef.resolver) != any(symCtx.resolver) {
		t.Fatalf("find_symbol_context does not share go_to_definition's analyzer instance")
	}

	findRefTool, ok := reg.Get("find_references")
	if !ok {
		t.Fatal("find_references not registered")
	}
	findRef, ok := findRefTool.(*findReferencesTool)
	if !ok {
		t.Fatal("find_references is not *findReferencesTool")
	}
	if any(findRef.finder) != any(symCtx.resolver) {
		t.Fatalf("find_symbol_context does not share find_references' analyzer instance")
	}
}

// TestFindSymbolContextDoesNotCallModelFacingToolsOrIntroduceSecondAnalyzer
// proves find_symbol_context has no runtime dependency on the three existing
// nav tools being registered - it disables all three and confirms
// find_symbol_context still resolves correctly, which would be impossible if
// it called them as tools (Design: "do not call model-facing tools from
// another model-facing tool", reaffirmed for this tool in the follow-on
// section's rollback criterion).
func TestFindSymbolContextDoesNotCallModelFacingToolsOrIntroduceSecondAnalyzer(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, ws.Abs, "go.mod", "module example.com/widget\n\ngo 1.21\n")
	writeFile(t, ws.Abs, "widget.go", "package widget\n\nfunc Widget() {}\n")

	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:    ws,
		RunAllowlist: testRunAllowlist,
		DisableTools: []string{"go_to_definition", "find_references", "list_symbols"},
	})
	for _, name := range []string{"go_to_definition", "find_references", "list_symbols"} {
		if _, ok := reg.Get(name); ok {
			t.Fatalf("%s must be disabled for this test to be meaningful", name)
		}
	}
	if _, ok := reg.Get("find_symbol_context"); !ok {
		t.Fatal("find_symbol_context must remain registered when the three existing nav tools are disabled")
	}

	raw, err := reg.Execute(context.Background(), "find_symbol_context", json.RawMessage(`{"symbol":"Widget"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out findSymbolContextOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.SymbolAvailable || out.Definition == nil {
		t.Fatalf("find_symbol_context could not resolve Widget with the other nav tools disabled: %+v", out)
	}
}

func TestFindSymbolContextOutputIsValidJSONAndWithinBudget(t *testing.T) {
	ws := navWorkspace(t)
	resolver := &fakeSymbolContextResolver{
		fakeDefinitionResolver: fakeDefinitionResolver{def: codeintel.Definition{Symbol: "Widget", Kind: codeintel.KindFunc, Path: ws.Abs + "/w.go", Line: 1, EndLine: 1, Signature: "func Widget()"}},
		fakeReferenceFinder:    fakeReferenceFinder{result: codeintel.Result{Symbol: "Widget", Locations: manyLocations(40), Complete: true}},
	}
	tool := &findSymbolContextTool{ws: ws, resolver: resolver, maxBytes: 512}
	raw, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget","max_references":40}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("output is not valid JSON: %s", raw)
	}
	if len(raw) > 512 {
		t.Fatalf("output len=%d exceeds configured budget 512", len(raw))
	}
}

func TestFindSymbolContextCapabilityUsesStableReadKey(t *testing.T) {
	_, reg := setupWS(t)
	tool, ok := reg.Get("find_symbol_context")
	if !ok {
		t.Fatal("find_symbol_context not registered")
	}
	capable, ok := tool.(CapableTool)
	if !ok {
		t.Fatal("find_symbol_context does not implement CapableTool")
	}
	args := json.RawMessage(`{"symbol":"pkg.Widget","max_references":10,"context_lines":5}`)
	c1 := capable.Capability(args)
	c2 := capable.Capability(args)
	if c1.Class != ExecutionRead {
		t.Fatalf("Class=%v, want ExecutionRead", c1.Class)
	}
	if c1.ResourceKey == "" || c1.ResourceKey != c2.ResourceKey {
		t.Fatalf("ResourceKey not stable: %q vs %q", c1.ResourceKey, c2.ResourceKey)
	}
	other := capable.Capability(json.RawMessage(`{"symbol":"pkg.Other","max_references":10,"context_lines":5}`))
	if other.ResourceKey == c1.ResourceKey {
		t.Fatalf("different symbols produced the same resource key")
	}
	differentBounds := capable.Capability(json.RawMessage(`{"symbol":"pkg.Widget","max_references":11,"context_lines":5}`))
	if differentBounds.ResourceKey == c1.ResourceKey {
		t.Fatalf("different max_references produced the same resource key")
	}
}
