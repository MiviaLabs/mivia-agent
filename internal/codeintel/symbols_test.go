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
