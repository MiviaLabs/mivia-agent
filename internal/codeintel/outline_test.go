package codeintel

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const outlineFixture = `package nav

import "fmt"

// Base is embedded below.
type Base struct {
	ID int
}

// Widget is a thing.
type Widget struct {
	Base
	Name string
	size int
}

// Label returns the name.
func (w *Widget) Label() string { return w.Name }

func (w Widget) valueRecv() int { return w.size }

const (
	MaxWidgets = 7
	minWidgets = 1
)

var defaultWidget = Widget{}

// BuildWidget makes one.
func BuildWidget(name string, n int) (*Widget, error) {
	if name == "" {
		return nil, fmt.Errorf("empty")
	}
	return &Widget{Name: name}, nil
}
`

func writeOutlineFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outline.go")
	write(t, path, outlineFixture)
	return path
}

func findSymbol(syms []Symbol, name string, kind SymbolKind) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name && s.Kind == kind {
			return s, true
		}
	}
	return Symbol{}, false
}

// TestFileOutlineKindsAndReceivers pins the outline's per-symbol contract:
// kind, receiver, exported flag, and a one-line signature.
func TestFileOutlineKindsAndReceivers(t *testing.T) {
	res, err := FileOutline(writeOutlineFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		kind     SymbolKind
		receiver string
		exported bool
	}{
		{"Widget", KindType, "", true},
		{"Name", KindField, "Widget", true},
		{"size", KindField, "Widget", false},
		{"Base", KindField, "Widget", true},
		{"Label", KindMethod, "*Widget", true},
		{"valueRecv", KindMethod, "Widget", false},
		{"MaxWidgets", KindConst, "", true},
		{"minWidgets", KindConst, "", false},
		{"defaultWidget", KindVar, "", false},
		{"BuildWidget", KindFunc, "", true},
	}
	for _, c := range cases {
		got, ok := findSymbol(res.Symbols, c.name, c.kind)
		if !ok {
			t.Errorf("outline missing %s %s", c.kind, c.name)
			continue
		}
		if got.Receiver != c.receiver {
			t.Errorf("%s receiver = %q, want %q", c.name, got.Receiver, c.receiver)
		}
		if got.Exported != c.exported {
			t.Errorf("%s exported = %v, want %v", c.name, got.Exported, c.exported)
		}
		if got.Line <= 0 || got.EndLine < got.Line {
			t.Errorf("%s span = %d..%d, want a positive ascending span", c.name, got.Line, got.EndLine)
		}
		if strings.ContainsAny(got.Signature, "\n\t") {
			t.Errorf("%s signature is not one line: %q", c.name, got.Signature)
		}
	}
}

// TestFileOutlineSignatureOmitsBody keeps outlines cheap: a function's shape is
// the product, its implementation is not.
func TestFileOutlineSignatureOmitsBody(t *testing.T) {
	res, err := FileOutline(writeOutlineFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	fn, ok := findSymbol(res.Symbols, "BuildWidget", KindFunc)
	if !ok {
		t.Fatal("BuildWidget missing")
	}
	if !strings.Contains(fn.Signature, "func BuildWidget(name string, n int) (*Widget, error)") {
		t.Errorf("signature = %q", fn.Signature)
	}
	if strings.Contains(fn.Signature, "fmt.Errorf") {
		t.Errorf("signature carries the body: %q", fn.Signature)
	}
	if fn.EndLine <= fn.Line {
		t.Errorf("multi-line function reported span %d..%d", fn.Line, fn.EndLine)
	}
}

// TestFileOutlineIsOrderedByPosition pins the file-mode ordering guarantee.
func TestFileOutlineIsOrderedByPosition(t *testing.T) {
	res, err := FileOutline(writeOutlineFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	prev := 0
	for _, s := range res.Symbols {
		if s.Line < prev {
			t.Fatalf("symbol %q at line %d follows line %d - outline is not in source order", s.Name, s.Line, prev)
		}
		prev = s.Line
	}
}

// TestFileOutlineNeedsNoWorkspace is D2's whole point: the outline path parses
// one file, so it works with no module, no go.mod, and no type checking.
func TestFileOutlineNeedsNoWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lonely.go")
	// Imports a package that does not exist: a type-checked path would fail.
	write(t, path, "package lonely\n\nimport \"example.com/nope\"\n\nfunc Do() nope.T { return nil }\n")

	res, err := FileOutline(path)
	if err != nil {
		t.Fatalf("outline of an unbuildable file failed: %v", err)
	}
	if _, ok := findSymbol(res.Symbols, "Do", KindFunc); !ok {
		t.Fatal("Do missing from the outline")
	}
}

// TestFileOutlineReportsParseErrors: a file that does not parse is an error,
// not a silently empty outline.
func TestFileOutlineReportsParseErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.go")
	write(t, path, "package broken\n\nfunc Oops( {\n")
	if _, err := FileOutline(path); err == nil {
		t.Fatal("expected an error for an unparseable file")
	}
}

// TestFileOutlineRefusesUnsupportedFileType: a file this backend cannot read
// gets the shared analysis-unavailable answer, not a parser error phrased in
// one language's grammar - "unsupported" and "broken" are different facts.
func TestFileOutlineRefusesUnsupportedFileType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script.py")
	write(t, path, "def do():\n    return 1\n")

	_, err := FileOutline(path)
	if err == nil {
		t.Fatal("expected an unavailable error for an unsupported file type")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error %v does not wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "analysis unavailable") {
		t.Fatalf("error = %v, want the shared analysis-unavailable shape", err)
	}
}
