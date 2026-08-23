package sdkadapter

// Tests for the CLI-to-SDK tool-registry converter.

import (
	"bytes"
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

// TestConvertToolRegistryWithAdmissionStagedReturnsDenial asserts
// that when the StagedMessage predicate answers true for a tool, the
// wrapped tool's Run returns the staged message wrapped as a string
// without invoking the inner CLI tool. The Run call's inner-exec
// counter must stay zero.
func TestConvertToolRegistryWithAdmissionStagedReturnsDenial(t *testing.T) {
	reg := tools.NewRegistry()
	inner := &countingCLITool{name: "stagedtool", exec: "real result"}
	reg.Register(inner)
	got, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		StagedMessage: func(name string) (string, bool) {
			if name == "stagedtool" {
				return "tool staged; retry next turn", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("ConvertToolRegistryWithAdmission: %v", err)
	}
	wrapped, ok := got.Get("stagedtool")
	if !ok {
		t.Fatal("SDK registry missing stagedtool")
	}
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, ok := out.Value.(string)
	if !ok || s != "tool staged; retry next turn" {
		t.Fatalf("Run returned %v, want the staged denial string", out.Value)
	}
	if inner.calls != 0 {
		t.Fatalf("inner tool invoked %d times; want 0 (the admission check should short-circuit before Run)", inner.calls)
	}
}

// TestConvertToolRegistryWithAdmissionUnadmittedReturnsDenial asserts
// the UnadmittedHandler path: a handler that answers true runs in
// place of the inner tool, the returned message becomes the Run
// value, and the auto-stage side effect (here: a counter increment)
// fires exactly once per call.
func TestConvertToolRegistryWithAdmissionUnadmittedReturnsDenial(t *testing.T) {
	reg := tools.NewRegistry()
	inner := &countingCLITool{name: "admittool", exec: "real result"}
	reg.Register(inner)
	var staged int
	got, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		UnadmittedHandler: func(_ context.Context, name string) (string, bool) {
			if name == "admittool" {
				staged++
				return "tool unadmitted; admission in progress", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("ConvertToolRegistryWithAdmission: %v", err)
	}
	wrapped, _ := got.Get("admittool")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, ok := out.Value.(string)
	if !ok || s != "tool unadmitted; admission in progress" {
		t.Fatalf("Run returned %v, want the unadmitted denial string", out.Value)
	}
	if inner.calls != 0 {
		t.Fatalf("inner tool invoked %d times; want 0", inner.calls)
	}
	if staged != 1 {
		t.Fatalf("auto-stage fired %d times; want 1 per call", staged)
	}
}

// TestConvertToolRegistryWithAdmissionPassesThrough asserts that when
// neither predicate answers true, Run invokes the inner CLI tool the
// same way the plain ConvertToolRegistry does.
func TestConvertToolRegistryWithAdmissionPassesThrough(t *testing.T) {
	reg := tools.NewRegistry()
	inner := &countingCLITool{name: "open", exec: "real result"}
	reg.Register(inner)
	got, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		StagedMessage:     func(string) (string, bool) { return "", false },
		UnadmittedHandler: func(context.Context, string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("ConvertToolRegistryWithAdmission: %v", err)
	}
	wrapped, _ := got.Get("open")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, ok := out.Value.(string)
	if !ok || s != "real result" {
		t.Fatalf("Run returned %v, want the inner tool's real result", out.Value)
	}
	if inner.calls != 1 {
		t.Fatalf("inner tool invoked %d times; want 1", inner.calls)
	}
}

// TestConvertToolRegistryWithAdmissionNilPredicatesFallsBack asserts
// that a zero AdmissionPredicates produces the same result as
// ConvertToolRegistry: every tool is wrapped without admission
// checks and Run forwards to the inner CLI tool.
func TestConvertToolRegistryWithAdmissionNilPredicatesFallsBack(t *testing.T) {
	reg := tools.NewRegistry()
	inner := &countingCLITool{name: "plain", exec: "real result"}
	reg.Register(inner)
	got, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{})
	if err != nil {
		t.Fatalf("ConvertToolRegistryWithAdmission: %v", err)
	}
	wrapped, _ := got.Get("plain")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s, _ := out.Value.(string); s != "real result" {
		t.Fatalf("Run returned %v, want the inner result (nil predicates should fall through)", out.Value)
	}
}

// countingCLITool tracks Execute call counts so admission tests can
// assert the inner tool is bypassed when a predicate answers true.
type countingCLITool struct {
	name   string
	exec   string
	calls  int
	params map[string]any
}

func (c *countingCLITool) Name() string               { return c.name }
func (c *countingCLITool) Description() string        { return "counting tool" }
func (c *countingCLITool) Parameters() map[string]any { return c.params }
func (c *countingCLITool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	c.calls++
	return c.exec, nil
}

// TestRelaxTopLevelAdditionalPropertiesUnmarshalFailure pins
// sdkadapter/tool_registry.go:166-168: a schema that contains the
// "additionalProperties" key but is not valid JSON unmarshals as
// err != nil and the relaxer returns the schema unchanged.
func TestRelaxTopLevelAdditionalPropertiesUnmarshalFailure(t *testing.T) {
	// An object-shaped string is valid JSON but map[string]any
	// unmarshalling sees a non-object type. json.Unmarshal of an
	// object into map[string]any succeeds; we need INVALID JSON.
	garbled := []byte(`{"additionalProperties": false, malformed`)
	got := relaxTopLevelAdditionalProperties(garbled)
	if !bytes.Equal(got, garbled) {
		t.Fatalf("schema changed on garbled input")
	}
}

// TestRelaxTopLevelAdditionalPropertiesNonBoolEarlyReturn pins
// sdkadapter/tool_registry.go:170-172: a schema whose
// "additionalProperties" is not the bool `false` (e.g. an object, a
// string, or a number) returns the schema as-is.
func TestRelaxTopLevelAdditionalPropertiesNonBoolEarlyReturn(t *testing.T) {
	input := []byte(`{"type":"object","additionalProperties":{"type":"string"}}`)
	got := relaxTopLevelAdditionalProperties(input)
	if !bytes.Equal(got, input) {
		t.Fatalf("schema changed when additionalProperties was not bool false: %s", got)
	}
}

// TestRelaxTopLevelAdditionalPropertiesTrueEarlyReturn pins the same
// branch with the JSON literal `true`, which is the most common
// non-`false` boolean value.
func TestRelaxTopLevelAdditionalPropertiesTrueEarlyReturn(t *testing.T) {
	input := []byte(`{"type":"object","additionalProperties":true}`)
	got := relaxTopLevelAdditionalProperties(input)
	if !bytes.Equal(got, input) {
		t.Fatalf("schema changed when additionalProperties was true: %s", got)
	}
}

// TestRelaxTopLevelAdditionalPropertiesAbsentEarlyReturn pins
// sdkadapter/tool_registry.go:163-165: a schema with no
// "additionalProperties" key returns the schema as-is. This is the
// common case for most tool parameters.
func TestRelaxTopLevelAdditionalPropertiesAbsentEarlyReturn(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	got := relaxTopLevelAdditionalProperties(input)
	if !bytes.Equal(got, input) {
		t.Fatalf("absent additionalProperties changed: %s", got)
	}
}

// TestRelaxTopLevelAdditionalPropertiesRemoveKeyAndMarshal pins
// sdkadapter/tool_registry.go:173-177: when the JSON unmarshals
// successfully and the key is bool false, the relaxer deletes the
// key and re-marshals. Assert the deletion by examining the resulting
// bytes.
func TestRelaxTopLevelAdditionalPropertiesRemoveKeyAndMarshal(t *testing.T) {
	input := []byte(`{"type":"object","properties":{},"additionalProperties":false}`)
	got := relaxTopLevelAdditionalProperties(input)
	if bytes.Contains(got, []byte("additionalProperties")) {
		t.Fatalf("additionalProperties key not removed: %s", got)
	}
	if !bytes.Contains(got, []byte(`"type":"object"`)) {
		t.Fatalf("other keys lost during relax: %s", got)
	}
}
