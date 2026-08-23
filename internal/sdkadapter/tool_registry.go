// Package sdkadapter - CLI-to-SDK tool-registry converter.
//
// The CLI's internal/tools.Registry and the SDK's tools.Registry are
// distinct types in distinct modules. The SDK loop consumes only the
// SDK shape, so the bridge converts the CLI registry: every CLI tool
// wraps as an SDK tool plus tools.SchemaTool.
//
// SchemaTool is required, not optional: the SDK's Definitions helper
// fails closed with ErrNoSchemas when a non-empty registry holds no
// schema-publishing tool, so a Name/Run-only wrapper would make every
// non-empty conversion unusable. The schema is the json.Marshal of the
// CLI tool's Parameters() map - the same OpenAI-parameters object the
// CLI's OpenAITools() publishes today.
//
// The Execute call below is the bridge's entire purpose: this package
// is the sanctioned SDK-boundary seam (not agent orchestration), so
// the semgrep rule that routes agent-package execution through the
// runtime Dispatcher does not apply here. The dispatcher-side
// concerns the legacy path enforces (per-tool output ceilings, hooks,
// result shaping) are the chat-surface wiring's responsibility, not
// the type converter's.
package sdkadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// sdkToolAdapter wraps one CLI tools.Tool as the SDK's tools.Tool and
// tools.SchemaTool. Run marshals the SDK's InOut.Value to the JSON
// arguments the CLI's Execute expects, and wraps the CLI's string
// result in the SDK's Out.
type sdkToolAdapter struct {
	cli    tools.Tool
	schema []byte
}

// Compile-time assertions: the adapter satisfies both SDK interfaces.
var _ sdktools.Tool = (*sdkToolAdapter)(nil)
var _ sdktools.SchemaTool = (*sdkToolAdapter)(nil)

// newSDKToolAdapter wraps one CLI tool, publishing its parameter
// schema. A Parameters() map that fails to marshal is a programmer
// error in the tool; the adapter returns it rather than dropping the
// schema silently, because a schema-less tool would trip the SDK's
// ErrNoSchemas at New time with a less actionable message.
func newSDKToolAdapter(t tools.Tool) (*sdkToolAdapter, error) {
	schema, err := json.Marshal(t.Parameters())
	if err != nil {
		return nil, fmt.Errorf("sdkadapter: tool %q: marshal parameters: %w", t.Name(), err)
	}
	return &sdkToolAdapter{cli: t, schema: schema}, nil
}

// Name implements tools.Tool. It forwards to the wrapped CLI tool.
func (s *sdkToolAdapter) Name() string { return s.cli.Name() }

// Run implements tools.Tool. It marshals in.Value to the JSON
// arguments the CLI's Execute consumes and wraps the string result.
// A nil in.Value marshals to "null"; the CLI's Execute treats
// non-object JSON as invalid arguments, matching its own registry's
// Execute validation.
func (s *sdkToolAdapter) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	args, err := json.Marshal(in.Value)
	if err != nil {
		return sdktools.Out{}, fmt.Errorf("sdkadapter: tool %q: marshal arguments: %w", s.cli.Name(), err)
	}
	result, err := s.cli.Execute(ctx, args)
	if err != nil {
		return sdktools.Out{}, err
	}
	return sdktools.Out{Value: result}, nil
}

// ParameterSchema implements tools.SchemaTool. It returns the JSON
// schema captured at wrap time from the CLI tool's Parameters() map.
func (s *sdkToolAdapter) ParameterSchema() []byte { return s.schema }

// DecodeArguments implements tools.SchemaTool. It validates that raw
// is well-formed JSON and returns it as a json.RawMessage payload so
// Run's marshal step emits the identical bytes the model produced -
// a byte-faithful pass-through with no intermediate shape that could
// reorder keys or renumber floats.
func (s *sdkToolAdapter) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	if !json.Valid(raw) {
		return sdktools.InOut{}, fmt.Errorf("sdkadapter: tool %q: arguments are not valid JSON", s.cli.Name())
	}
	return sdktools.InOut{Value: json.RawMessage(raw)}, nil
}

// ConvertToolRegistry converts a CLI registry to an SDK registry. A
// nil input returns a nil output; the SDK's Options.Validate reports
// ErrNoTools for the nil registry, which names the real problem. Add
// errors (blank name, duplicate name) wrap with the offending tool's
// name so the operator can find the duplicate.
func ConvertToolRegistry(cliReg *tools.Registry) (*sdktools.Registry, error) {
	if cliReg == nil {
		return nil, nil
	}
	sdkReg := sdktools.New()
	for _, t := range cliReg.List() {
		wrapped, err := newSDKToolAdapter(t)
		if err != nil {
			return nil, err
		}
		if err := sdkReg.Add(wrapped); err != nil {
			return nil, fmt.Errorf("sdkadapter: add tool %q to SDK registry: %w", t.Name(), err)
		}
	}
	return sdkReg, nil
}
