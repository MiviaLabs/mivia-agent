package sdkadapter

// Tests for the CLI-to-SDK tool-registry converter.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// fakeCLITool satisfies the CLI's tools.Tool with stub values.
type fakeCLITool struct {
	name   string
	params map[string]any
	exec   string
}

func (f *fakeCLITool) Name() string               { return f.name }
func (f *fakeCLITool) Description() string        { return "fake tool" }
func (f *fakeCLITool) Parameters() map[string]any { return f.params }
func (f *fakeCLITool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return f.exec, nil
}

// TestConvertToolRegistryWrapsAllTools asserts the converter carries
// every registered CLI tool into the SDK registry by name.
func TestConvertToolRegistryWrapsAllTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeCLITool{name: "alpha", params: map[string]any{"type": "object"}})
	reg.Register(&fakeCLITool{name: "beta", params: map[string]any{"type": "object"}})
	got, err := ConvertToolRegistry(reg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, ok := got.Get(name); !ok {
			t.Errorf("SDK registry missing tool %q", name)
		}
	}
}

// TestConvertToolRegistrySchemaPublished asserts the wrapped tool
// implements tools.SchemaTool and its ParameterSchema unmarshals
// back to the CLI tool's Parameters() map. This guards the
// ErrNoSchemas trap: the SDK's Definitions fails closed when a
// non-empty registry holds no schema-publishing tool.
func TestConvertToolRegistrySchemaPublished(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&fakeCLITool{name: "schematool", params: map[string]any{"type": "object"}})
	got, err := ConvertToolRegistry(reg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	wrapped, ok := got.Get("schematool")
	if !ok {
		t.Fatal("SDK registry missing schematool")
	}
	st, ok := wrapped.(sdktools.SchemaTool)
	if !ok {
		t.Fatal("wrapped tool does not implement tools.SchemaTool")
	}
	var back map[string]any
	if err := json.Unmarshal(st.ParameterSchema(), &back); err != nil {
		t.Fatalf("ParameterSchema unmarshal: %v", err)
	}
	if back["type"] != "object" {
		t.Fatalf("schema = %v, want type=object", back)
	}
}

// TestConvertToolRegistryEmpty asserts an empty CLI registry converts
// to a non-nil empty SDK registry (Validate requires non-nil Tools).
func TestConvertToolRegistryEmpty(t *testing.T) {
	got, err := ConvertToolRegistry(tools.NewRegistry())
	if err != nil {
		t.Fatalf("ConvertToolRegistry(empty): %v", err)
	}
	if got == nil {
		t.Fatal("empty registry converted to nil; want non-nil empty registry")
	}
	if all := got.Tools(); len(all) != 0 {
		t.Fatalf("converted registry has %d tools, want 0", len(all))
	}
}

// TestConvertToolRegistryNil asserts a nil CLI registry converts to a
// nil SDK registry; the SDK's Validate reports ErrNoTools for nil,
// which names the real problem.
func TestConvertToolRegistryNil(t *testing.T) {
	got, err := ConvertToolRegistry(nil)
	if err != nil {
		t.Fatalf("ConvertToolRegistry(nil): %v", err)
	}
	if got != nil {
		t.Fatalf("nil registry converted to %v, want nil", got)
	}
}
