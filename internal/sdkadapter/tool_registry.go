// Package sdkadapter - CLI-to-SDK tool-registry converter.
//
// The CLI's internal/tools.Registry and the SDK's tools.Registry are
// distinct types in distinct modules. The SDK loop consumes only the
// SDK shape, so the bridge converts the CLI registry: every CLI tool
// wraps as an SDK tool plus tools.SchemaTool.
//
// SchemaTool is required, not optional: the SDK's Definitions helper
// fails closed with ErrNoSchemas when a non-empty registry holds no
// schema-publishing tool. The schema is the json.Marshal of the CLI
// tool's Parameters() map - the same OpenAI-parameters object the
// CLI's OpenAITools() publishes today.
//
// Admission-checked conversion: ConvertToolRegistryWithAdmission
// accepts optional StagedMessage and UnadmittedHandler predicates
// mirroring the legacy CLI's loop_tool_exec.go:13-27 checks. When a
// predicate answers true for a call, the wrapped tool returns the
// denial string wrapped in tools.Out, the SDK renders it as a
// RoleTool message, and the model retries on the next iteration.
// Per-call evaluation keeps the UnadmittedHandler auto-stage side
// effect (see internal/agent/options.go:108-117) firing only when
// the model actually invokes the unadmitted tool. See
// docs/development/sdk-backend-field-mapping.md for the wider
// rationale.
package sdkadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// AdmissionPredicates carries the optional per-call admission checks
// the legacy CLI applies in loop_tool_exec.go. Either predicate may
// be nil; both nil produces the same result as ConvertToolRegistry
// without admission checks.
type AdmissionPredicates struct {
	// StagedMessage answers whether name is a tool staged for
	// publication. The returned message becomes the RoleTool denial
	// content when true. nil disables the check.
	StagedMessage func(name string) (string, bool)
	// UnadmittedHandler answers whether name is a tool advertised but
	// not yet admitted for execution. The handler may auto-stage the
	// tool for publication at the next step boundary as a side
	// effect; the returned message becomes the RoleTool denial
	// content when true. nil disables the check.
	UnadmittedHandler func(ctx context.Context, name string) (string, bool)
}

// admissionCheckedToolAdapter wraps a CLI tool plus the admission
// predicates. Run checks StagedMessage and UnadmittedHandler first;
// if either answers true, Run returns the denial message wrapped in
// tools.Out so the SDK renders it as the RoleTool content. Otherwise
// Run delegates to the inner CLI tool the same way sdkToolAdapter
// does.
type admissionCheckedToolAdapter struct {
	inner      *sdkToolAdapter
	staged     func(name string) (string, bool)
	unadmitted func(ctx context.Context, name string) (string, bool)
}

var _ sdktools.Tool = (*admissionCheckedToolAdapter)(nil)
var _ sdktools.SchemaTool = (*admissionCheckedToolAdapter)(nil)

func (a *admissionCheckedToolAdapter) Name() string { return a.inner.cli.Name() }

func (a *admissionCheckedToolAdapter) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	name := a.inner.cli.Name()
	if a.staged != nil {
		if msg, ok := a.staged(name); ok {
			return sdktools.Out{Value: msg}, nil
		}
	}
	if a.unadmitted != nil {
		if msg, ok := a.unadmitted(ctx, name); ok {
			return sdktools.Out{Value: msg}, nil
		}
	}
	return a.inner.Run(ctx, in)
}

func (a *admissionCheckedToolAdapter) ParameterSchema() []byte { return a.inner.ParameterSchema() }

func (a *admissionCheckedToolAdapter) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return a.inner.DecodeArguments(raw)
}

// ConvertToolRegistryWithAdmission converts a CLI registry to an SDK
// registry, wrapping each tool with the admission predicates. A nil
// pred produces the same result as ConvertToolRegistry. The check
// runs at call time so UnadmittedHandler's auto-stage side effect
// fires only when the model actually invokes the unadmitted tool.
func ConvertToolRegistryWithAdmission(cliReg *tools.Registry, pred AdmissionPredicates) (*sdktools.Registry, error) {
	if cliReg == nil {
		return nil, nil
	}
	if pred.StagedMessage == nil && pred.UnadmittedHandler == nil {
		return ConvertToolRegistry(cliReg)
	}
	sdkReg := sdktools.New()
	for _, t := range cliReg.List() {
		inner, err := newSDKToolAdapter(t)
		if err != nil {
			return nil, err
		}
		wrapped := &admissionCheckedToolAdapter{
			inner:      inner,
			staged:     pred.StagedMessage,
			unadmitted: pred.UnadmittedHandler,
		}
		if err := sdkReg.Add(wrapped); err != nil {
			return nil, fmt.Errorf("sdkadapter: add tool %q to SDK registry: %w", t.Name(), err)
		}
	}
	return sdkReg, nil
}

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
