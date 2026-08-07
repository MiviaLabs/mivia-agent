package codeintel

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// symbolFixture writes a two-package module with a type, a method, an
// interface, an embedded field and cross-package use.
func symbolFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/nav\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "widget.go"), `package nav

import "example.com/nav/sub"

// Base carries the id.
type Base struct {
	ID int
}

// Widget is a thing.
type Widget struct {
	Base
	Name string
}

// Label returns the name.
func (w *Widget) Label() string {
	return w.Name
}

// Labeler is implemented by Widget.
type Labeler interface {
	Label() string
}

// BuildWidget makes one.
func BuildWidget(name string) *Widget {
	return &Widget{Name: name, Base: Base{ID: sub.NextID()}}
}
`)
	write(t, filepath.Join(dir, "sub", "sub.go"), `package sub

// NextID hands out ids.
func NextID() int { return 1 }

// SubWidget is unrelated to nav.Widget.
type SubWidget struct{}
`)
	return dir
}

// TestSymbolsPrefixSearch pins workspace mode: prefix match, kinds, package
// attribution, spans.
func TestSymbolsPrefixSearch(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	res, err := a.Symbols(context.Background(), "Widget", 0)
	if err != nil {
		t.Fatal(err)
	}
	sym, ok := findSymbol(res.Symbols, "Widget", KindType)
	if !ok {
		t.Fatalf("Widget not found; got %+v", res.Symbols)
	}
	if sym.Package != "example.com/nav" {
		t.Errorf("Widget package = %q", sym.Package)
	}
	if !strings.HasSuffix(sym.Path, "widget.go") {
		t.Errorf("Widget path = %q", sym.Path)
	}
	if sym.EndLine <= sym.Line {
		t.Errorf("Widget span %d..%d does not cover the declaration", sym.Line, sym.EndLine)
	}
	if !sym.Exported {
		t.Error("Widget should be exported")
	}
	for _, s := range res.Symbols {
		if !strings.HasPrefix(strings.ToLower(s.Name), "widget") {
			t.Errorf("result %q does not match the prefix", s.Name)
		}
	}
}

// TestSymbolsFindsMethods: methods are part of the navigable surface, with
// their receiver reported.
func TestSymbolsFindsMethods(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	res, err := a.Symbols(context.Background(), "Label", 0)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := findSymbol(res.Symbols, "Label", KindMethod)
	if !ok {
		t.Fatalf("method Label not found; got %+v", res.Symbols)
	}
	if m.Receiver != "*Widget" {
		t.Errorf("Label receiver = %q, want *Widget", m.Receiver)
	}
	if !strings.Contains(m.Signature, "func") || strings.Contains(m.Signature, "\n") {
		t.Errorf("Label signature = %q", m.Signature)
	}
	if _, ok := findSymbol(res.Symbols, "Labeler", KindType); !ok {
		t.Error("interface Labeler should also match the Label prefix")
	}
}

// TestSymbolsDedupsTestVariants: packages.Load reports a package and its
// test-augmented variant separately; one declaration must be listed once.
func TestSymbolsDedupsTestVariants(t *testing.T) {
	dir := symbolFixture(t)
	write(t, filepath.Join(dir, "widget_test.go"), `package nav

import "testing"

func TestNothing(t *testing.T) { _ = BuildWidget("x") }
`)
	a := NewAnalyzer(dir)
	res, err := a.Symbols(context.Background(), "BuildWidget", 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, s := range res.Symbols {
		if s.Name == "BuildWidget" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("BuildWidget listed %d times, want 1: %+v", count, res.Symbols)
	}
	for _, s := range res.Symbols {
		if strings.Contains(s.Package, "[") {
			t.Errorf("symbol %q reports the test-variant package id %q instead of the import path",
				s.Name, s.Package)
		}
	}
}

// TestSymbolsTruncatesAtLimit pins the cap and its flag.
func TestSymbolsTruncatesAtLimit(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	res, err := a.Symbols(context.Background(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) != 2 {
		t.Fatalf("got %d symbols, want 2", len(res.Symbols))
	}
	if !res.Truncated {
		t.Error("Truncated should be set when results were dropped")
	}
}

// TestSymbolsOrderIsStable: two runs against an unchanged workspace return the
// same list in the same order, which is what makes a limit meaningful.
func TestSymbolsOrderIsStable(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	first, err := a.Symbols(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Symbols(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Symbols) != len(second.Symbols) {
		t.Fatalf("result sizes differ: %d vs %d", len(first.Symbols), len(second.Symbols))
	}
	for i := range first.Symbols {
		if first.Symbols[i] != second.Symbols[i] {
			t.Fatalf("order differs at %d: %+v vs %+v", i, first.Symbols[i], second.Symbols[i])
		}
	}
}

// TestSymbolsUnavailableWithoutModule keeps the non-Go answer explicit rather
// than an empty success (D4).
func TestSymbolsUnavailableWithoutModule(t *testing.T) {
	a := NewAnalyzer(t.TempDir())
	_, err := a.Symbols(context.Background(), "Anything", 0)
	if err == nil {
		t.Fatal("expected an unavailable error with no module")
	}
	if !strings.Contains(err.Error(), "analysis unavailable") {
		t.Fatalf("error = %v, want the shared analysis-unavailable shape", err)
	}
}

// embeddedInterfaceFixture writes a two-package module with an interface that
// embeds another locally-defined interface (NumExplicitMethods==0, NumMethods>0)
// and a concrete implementor.
func embeddedInterfaceFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/embed\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "types.go"), `package embed

// Readable declares a single method.
type Readable interface {
	Read([]byte) (int, error)
}

// Labeler embeds Readable and adds no explicit methods.
type Labeler interface {
	Readable
}

// myReader implements Readable and therefore Labeler.
type myReader struct{}

func (myReader) Read([]byte) (int, error) { return 0, nil }
`)
	return dir
}

// TestAddMethodsFindsEmbeddedInterfaceMethods is a regression test for the
// bug where addMethods iterated iface.NumExplicitMethods() instead of
// iface.NumMethods(). For an interface composed entirely of embedded methods
// (e.g. Labeler embeds Readable), NumExplicitMethods() is 0, so the loop never
// executed and no methods were reported. Searching with prefix "Read" should
// find the Read method on the Labeler interface via embedding.
func TestAddMethodsFindsEmbeddedInterfaceMethods(t *testing.T) {
	a := NewAnalyzer(embeddedInterfaceFixture(t))
	res, err := a.Symbols(context.Background(), "Read", 0)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := findSymbol(res.Symbols, "Read", KindMethod)
	if !ok {
		t.Fatalf("method Read not found among symbols; got: %+v", res.Symbols)
	}
	if m.Receiver == "" {
		t.Errorf("Read should have a receiver (interface method), got empty receiver")
	}
}

// TestAddMethodsReportsAllEmbeddedMethods verifies that searching with prefix
// "Label" finds the Labeler type. The Read method (embedded from Readable) is
// found separately by searching with prefix "Read". Before the fix, only the
// Labeler type was found because addMethods reported zero methods for
// purely-embedded interfaces, so the embedded Read method was invisible to
// any prefix query.
func TestAddMethodsReportsAllEmbeddedMethods(t *testing.T) {
	a := NewAnalyzer(embeddedInterfaceFixture(t))
	res, err := a.Symbols(context.Background(), "Label", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findSymbol(res.Symbols, "Labeler", KindType); !ok {
		t.Fatalf("Labeler type not found; got: %+v", res.Symbols)
	}
	// The Read method should NOT appear under "Label" prefix since "Read" does
	// not start with "Label". But searching with prefix "Read" should find it.
	resRead, err := a.Symbols(context.Background(), "Read", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findSymbol(resRead.Symbols, "Read", KindMethod); !ok {
		t.Fatalf("Read method not found under Read prefix; got: %+v", resRead.Symbols)
	}
}

// TestSymbolsSkipsEmptyPrefixOnNonModule confirms that Symbols returns
// ErrUnavailable when called on a directory without a go.mod, even with an
// empty prefix (which normally matches everything).
func TestSymbolsSkipsEmptyPrefixOnNonModule(t *testing.T) {
	a := NewAnalyzer(t.TempDir())
	_, err := a.Symbols(context.Background(), "", 0)
	if err == nil {
		t.Fatal("expected ErrUnavailable for workspace without go.mod")
	}
	if !strings.Contains(err.Error(), "analysis unavailable") {
		t.Fatalf("error = %v, want analysis unavailable", err)
	}
}
