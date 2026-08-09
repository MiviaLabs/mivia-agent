package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestIntegrationFindSymbolContextCompletesSymbolEvidenceInOneToolCall proves
// the turn reduction find_symbol_context exists for (plan 66, follow-on #1):
// answering "what does this symbol do and who calls it" used to cost a
// list_symbols/go_to_definition call plus a separate find_references call.
// Here one find_symbol_context call, through the real agent loop, the real
// filesystem, and the real shared codeintel.Analyzer, must deliver both.
func TestIntegrationFindSymbolContextCompletesSymbolEvidenceInOneToolCall(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "looking up Widget",
			toolCalls: []provider.ToolCall{toolCall("call_symctx", "find_symbol_context",
				`{"symbol":"widget.Widget","max_references":10,"context_lines":5}`)},
		},
		{content: "found it"},
	})
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "go.mod"), []byte("module example.com/widget\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "widget.go"), []byte(
		"package widget\n\nfunc Widget() int {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "caller.go"), []byte(
		"package widget\n\nfunc UseWidget() int {\n\treturn Widget()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := runToolStep(t, h, "look up Widget's definition and callers", "call_symctx")

	var out struct {
		Symbol     string `json:"symbol"`
		Definition struct {
			Path      string `json:"path"`
			Line      int    `json:"line"`
			EndLine   int    `json:"end_line"`
			Signature string `json:"signature"`
			Source    string `json:"source"`
		} `json:"definition"`
		References []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Role string `json:"role"`
		} `json:"references"`
		ReferenceCount  int    `json:"reference_count"`
		SymbolAvailable bool   `json:"symbol_available"`
		Error           string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("tool result is not valid JSON: %v\nbody=%s", err, body)
	}
	if !out.SymbolAvailable || out.Error != "" {
		t.Fatalf("expected a resolved symbol, got %+v (body=%s)", out, body)
	}
	if out.Definition.Path != "widget.go" || out.Definition.Line != 3 {
		t.Fatalf("definition location = %+v, want widget.go:3", out.Definition)
	}
	if out.Definition.Source == "" {
		t.Fatalf("expected declaration source in the single call, got none: %+v", out.Definition)
	}
	// The one call must have carried the caller location too - the part that
	// previously required a second, separate find_references call.
	if out.ReferenceCount != 1 || len(out.References) != 1 {
		t.Fatalf("reference_count/references=%+v, want exactly 1", out)
	}
	ref := out.References[0]
	if ref.Path != "caller.go" || ref.Line != 4 || ref.Role != "return" {
		t.Fatalf("reference = %+v, want caller.go:4 role=return", ref)
	}
}
