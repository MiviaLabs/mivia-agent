package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
)

type fakeDefinitionResolver struct {
	def        codeintel.Definition
	err        error
	lastSymbol string
}

func (f *fakeDefinitionResolver) Definition(ctx context.Context, symbol string) (codeintel.Definition, error) {
	f.lastSymbol = symbol
	return f.def, f.err
}

func decodeDefinition(t *testing.T, out string) goToDefinitionResult {
	t.Helper()
	var got goToDefinitionResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	return got
}

func TestGoToDefinitionRequiresSymbol(t *testing.T) {
	tool := &goToDefinitionTool{ws: navWorkspace(t), resolver: &fakeDefinitionResolver{}, maxBytes: 10000}
	for _, args := range []string{`{}`, `{"symbol":""}`, `{"symbol":"   "}`, `not json`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("expected an error for %s", args)
		}
	}
}

func TestGoToDefinitionNoAnalyzerIsOutputNotFailure(t *testing.T) {
	tool := &goToDefinitionTool{ws: navWorkspace(t), resolver: nil, maxBytes: 10000}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no analyzer available") {
		t.Fatalf("output = %s", out)
	}
}

func TestGoToDefinitionUnavailableIsOutputNotFailure(t *testing.T) {
	tool := &goToDefinitionTool{
		ws:       navWorkspace(t),
		resolver: &fakeDefinitionResolver{err: fmt.Errorf("analysis unavailable: %w", codeintel.ErrUnavailable)},
		maxBytes: 10000,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"Widget"}`))
	if err != nil {
		t.Fatalf("availability errors belong in the output body: %v", err)
	}
	if got := decodeDefinition(t, out); !strings.Contains(got.Error, "analysis unavailable") {
		t.Fatalf("output = %s", out)
	}
}

func TestGoToDefinitionReportsRelativePaths(t *testing.T) {
	ws := navWorkspace(t)
	tool := &goToDefinitionTool{
		ws: ws,
		resolver: &fakeDefinitionResolver{def: codeintel.Definition{
			Symbol: "pkg.Widget", Kind: codeintel.KindType,
			Path: filepath.Join(ws.Abs, "pkg", "widget.go"), Line: 3, EndLine: 5,
			Signature: "type Widget struct{...}", Source: "type Widget struct {\n\tName string\n}",
		}},
		maxBytes: 10000,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"pkg.Widget"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeDefinition(t, out)
	if got.Path != "pkg/widget.go" {
		t.Errorf("path = %q, want the workspace-relative form", got.Path)
	}
	if got.Kind != "type" || got.Line != 3 || got.EndLine != 5 {
		t.Errorf("result = %+v", got)
	}
}

// TestGoToDefinitionSelfTruncatesOversizedSource pins the budget: an oversized
// declaration loses SOURCE LINES, never the position the model navigates by,
// and the output stays valid JSON inside the bound.
func TestGoToDefinitionSelfTruncatesOversizedSource(t *testing.T) {
	ws := navWorkspace(t)
	var src strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&src, "\tline %d %s\n", i, strings.Repeat("x", 200))
	}
	resolver := &fakeDefinitionResolver{def: codeintel.Definition{
		Symbol: "big.Long", Kind: codeintel.KindFunc,
		Path: filepath.Join(ws.Abs, "big.go"), Line: 10, EndLine: 400,
		Signature: "func Long() int", Source: src.String(), SourceTruncated: true,
	}}

	for _, budget := range []int{4000, 1000, 400, 150} {
		tool := &goToDefinitionTool{ws: ws, resolver: resolver, maxBytes: budget}
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"symbol":"big.Long"}`))
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if len(out) > budget {
			t.Errorf("budget %d: output is %d bytes", budget, len(out))
		}
		got := decodeDefinition(t, out)
		if !got.SourceTruncated {
			t.Errorf("budget %d: truncation not reported", budget)
		}
		if budget >= 400 && (got.Line != 10 || got.Path == "") {
			t.Errorf("budget %d: dropped the position instead of source text: %+v", budget, got)
		}
	}
}

func TestGoToDefinitionIsReadOnly(t *testing.T) {
	tool := &goToDefinitionTool{maxBytes: 2048}
	if cap := tool.Capability(nil); cap.Class != ExecutionRead {
		t.Fatalf("class = %v, want ExecutionRead", cap.Class)
	}
	if tool.ResultBudgetBytes() != 2048 {
		t.Fatalf("declared budget = %d", tool.ResultBudgetBytes())
	}
}
