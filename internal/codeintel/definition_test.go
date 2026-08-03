package codeintel

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefinitionAcrossPackages resolves a qualified symbol in another package
// and returns its span and source.
func TestDefinitionAcrossPackages(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	def, err := a.Definition(context.Background(), "sub.NextID")
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindFunc {
		t.Errorf("kind = %q, want func", def.Kind)
	}
	if def.Package != "example.com/nav/sub" {
		t.Errorf("package = %q", def.Package)
	}
	if !strings.HasSuffix(def.Path, filepath.Join("sub", "sub.go")) {
		t.Errorf("path = %q", def.Path)
	}
	if !strings.Contains(def.Source, "func NextID() int { return 1 }") {
		t.Errorf("source = %q", def.Source)
	}
	if def.Line <= 0 || def.EndLine < def.Line {
		t.Errorf("span = %d..%d", def.Line, def.EndLine)
	}
}

// TestDefinitionOfMethod resolves Type.Method, which plain package-scope
// lookup cannot see.
func TestDefinitionOfMethod(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	def, err := a.Definition(context.Background(), "Widget.Label")
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindMethod {
		t.Errorf("kind = %q, want method", def.Kind)
	}
	if def.Receiver != "*Widget" {
		t.Errorf("receiver = %q", def.Receiver)
	}
	if !strings.Contains(def.Source, "func (w *Widget) Label() string {") {
		t.Errorf("source = %q", def.Source)
	}
}

// TestDefinitionOfEmbeddedField resolves a field promoted from an embedded
// type: the definition site is the embedded declaration, not the embedder.
func TestDefinitionOfEmbeddedField(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	def, err := a.Definition(context.Background(), "Widget.ID")
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindField {
		t.Errorf("kind = %q, want field", def.Kind)
	}
	if !strings.Contains(def.Source, "ID int") {
		t.Errorf("source = %q, want the embedded declaration", def.Source)
	}
}

// TestDefinitionOfDeclaredField resolves a field declared on the type itself.
func TestDefinitionOfDeclaredField(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	def, err := a.Definition(context.Background(), "Widget.Name")
	if err != nil {
		t.Fatal(err)
	}
	if def.Kind != KindField {
		t.Errorf("kind = %q, want field", def.Kind)
	}
	if !strings.Contains(def.Source, "Name string") {
		t.Errorf("source = %q", def.Source)
	}
	if def.EndLine != def.Line {
		t.Errorf("single-line field reported span %d..%d", def.Line, def.EndLine)
	}
}

// TestDefinitionSourceIsBounded pins the 40-line bound and its flag.
func TestDefinitionSourceIsBounded(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/big\n\ngo 1.22\n")
	var body strings.Builder
	body.WriteString("package big\n\n// Long is long.\nfunc Long() int {\n\tn := 0\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&body, "\tn += %d\n", i)
	}
	body.WriteString("\treturn n\n}\n")
	write(t, filepath.Join(dir, "big.go"), body.String())

	a := NewAnalyzer(dir)
	def, err := a.Definition(context.Background(), "Long")
	if err != nil {
		t.Fatal(err)
	}
	if !def.SourceTruncated {
		t.Error("SourceTruncated should be set for an over-long declaration")
	}
	if got := len(strings.Split(def.Source, "\n")); got != MaxDefinitionLines {
		t.Errorf("source has %d lines, want %d", got, MaxDefinitionLines)
	}
	if def.EndLine-def.Line+1 <= MaxDefinitionLines {
		t.Error("the reported span should still cover the whole declaration")
	}
}

// TestDefinitionAmbiguousIsRefused: a bare name matching two packages is an
// error naming both, never an arbitrary pick.
func TestDefinitionAmbiguousIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/amb\n\ngo 1.22\n")
	write(t, filepath.Join(dir, "a", "a.go"), "package a\n\ntype Dup struct{}\n")
	write(t, filepath.Join(dir, "b", "b.go"), "package b\n\ntype Dup struct{}\n")

	a := NewAnalyzer(dir)
	_, err := a.Definition(context.Background(), "Dup")
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v", err)
	}

	if _, err := a.Definition(context.Background(), "a.Dup"); err != nil {
		t.Fatalf("qualifying the symbol should resolve it: %v", err)
	}
}

// TestDefinitionNotFound reports a clear miss rather than an empty success.
func TestDefinitionNotFound(t *testing.T) {
	a := NewAnalyzer(symbolFixture(t))
	if _, err := a.Definition(context.Background(), "NoSuchSymbol"); err == nil {
		t.Fatal("expected a not-found error")
	}
	if _, err := a.Definition(context.Background(), "Widget.NoSuchMember"); err == nil {
		t.Fatal("expected a not-found error for an unknown member")
	}
}

// TestDefinitionUnavailableWithoutModule keeps the explicit unavailable shape.
func TestDefinitionUnavailableWithoutModule(t *testing.T) {
	a := NewAnalyzer(t.TempDir())
	_, err := a.Definition(context.Background(), "Anything")
	if err == nil || !strings.Contains(err.Error(), "analysis unavailable") {
		t.Fatalf("error = %v, want the shared analysis-unavailable shape", err)
	}
}

// TestDefinitionReadsSourceFromDiskAfterEdit is the staleness guarantee at the
// codeintel layer: an edit between two calls moves the reported position and
// the returned text with it.
func TestDefinitionReadsSourceFromDiskAfterEdit(t *testing.T) {
	dir := symbolFixture(t)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	before, err := a.Definition(ctx, "sub.NextID")
	if err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(dir, "sub", "sub.go"), `package sub

// A pushed-down declaration.
//
//

// NextID hands out ids.
func NextID() int { return 42 }

// SubWidget is unrelated to nav.Widget.
type SubWidget struct{}
`)

	after, err := a.Definition(ctx, "sub.NextID")
	if err != nil {
		t.Fatal(err)
	}
	if after.Line == before.Line {
		t.Fatalf("position did not move after the edit (both %d)", before.Line)
	}
	if !strings.Contains(after.Source, "return 42") {
		t.Fatalf("source = %q, want the edited body", after.Source)
	}
}
