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
// ConvertToolRegistryWithAdmission adds the legacy CLI's per-call
// staged/unadmitted predicates (see internal/agent/loop_tool_exec.go:13-27)
// on top of the standard wrapper: a predicate answering true
// returns a denial string wrapped in tools.Out, which the SDK
// renders as a RoleTool message so the model retries on the next
// iteration. Per-call evaluation keeps the UnadmittedHandler
// auto-stage side effect (see internal/agent/options.go:108-117)
// firing only when the model actually invokes the unadmitted tool.
//
// The ref-only shim lives in the agent package
// (internal/agent/refonly_shim.go) and is applied after this
// converter. It cannot live here because *remainder.Spool already
// imports sdkadapter for sdkadapter.Mint; placing the shim in
// sdkadapter would create an import cycle. See
// docs/development/sdk-backend-field-mapping.md for the wider
// rationale.
package sdkadapter

import (
	"bytes"
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
	// ApprovalGate, when non-nil, is invoked before the tool runs for
	// any tool whose internal Capability.Class >= tools.ExecutionWrite.
	// Read-class tools skip the gate. The verdict drives the wrap
	// layer added below: Approved true delegates to the inner CLI
	// tool; Approved false returns the denial Err as the RoleTool
	// content (mirroring the staged/unadmitted denial shape).
	ApprovalGate func(ctx context.Context, name string, args json.RawMessage) ApprovalResult
	// ApprovalStanding is consulted BEFORE ApprovalGate to honor
	// "always" decisions. The same instance must be shared across
	// legacy and SDK paths within one session so a "always" decision
	// persists across backends.
	ApprovalStanding *ApprovalStanding
	// ApprovalPolicy controls tool execution approval policy ("write-only", "auto", "always").
	ApprovalPolicy string
	// EmitPending publishes a "tool pending approval" advisory from
	// inside the SDK wrapper, before invoking the gate. The fields
	// are the bridge primitives so the wrapper does not need to
	// import internal/agent (the agent package imports sdkadapter;
	// reversing that direction would create a cycle). The caller
	// reconstructs an agent.Event and routes it through the same
	// emit path the legacy loop uses (OnEvent + EventBus). nil
	// disables the surface (the bridge still runs; the wrapper
	// just does not publish the pending event).
	//
	// toolCallID is the in-flight call's id (toolcallctx.ToolCall.ID)
	// or "" when context did not carry one. The host MUST thread it
	// into the model-visible EventToolPending.ToolCallID: a drop
	// strands the UI's approval resolver, which keys on this id,
	// and the gate's blocking select never fires - the tool hangs
	// silently after the user approves.
	EmitPending func(toolCallID, name, detail, input string)
}

// admissionCheckedToolAdapter wraps a CLI tool plus the admission
// predicates. Run checks StagedMessage and UnadmittedHandler first;
// if either answers true, Run returns the denial message wrapped in
// tools.Out so the SDK renders it as the RoleTool content. Otherwise
// Run delegates to the inner CLI tool the same way sdkToolAdapter
// does.
type admissionCheckedToolAdapter struct {
	inner      sdktools.Tool
	cliName    string
	staged     func(name string) (string, bool)
	unadmitted func(ctx context.Context, name string) (string, bool)
}

var _ sdktools.Tool = (*admissionCheckedToolAdapter)(nil)
var _ sdktools.SchemaTool = (*admissionCheckedToolAdapter)(nil)
var _ sdktools.ProfiledTool = (*admissionCheckedToolAdapter)(nil)

func (a *admissionCheckedToolAdapter) Name() string { return a.cliName }

// ExecutionProfile forwards the inner tool's profile. The SDK's
// run-timeout resolver consults only the OUTERMOST registered value,
// and Go interface wrappers silently strip optional interfaces, so
// every wrapper layer forwards explicitly; a profile-less inner yields
// the zero profile ("undeclared": the registry default applies).
func (a *admissionCheckedToolAdapter) ExecutionProfile() sdktools.ExecutionProfile {
	return sdktools.ExecutionProfileOf(a.inner)
}

func (a *admissionCheckedToolAdapter) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	name := a.cliName
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

func (a *admissionCheckedToolAdapter) ParameterSchema() []byte {
	if st, ok := a.inner.(sdktools.SchemaTool); ok {
		return st.ParameterSchema()
	}
	return nil
}

func (a *admissionCheckedToolAdapter) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	if st, ok := a.inner.(sdktools.SchemaTool); ok {
		return st.DecodeArguments(raw)
	}
	return sdktools.InOut{}, nil
}

// WrapToolWithAdmission wraps one already-converted SDK tool with
// approval and admission layers according to pred.
func WrapToolWithAdmission(inner sdktools.Tool, cliTool tools.Tool, pred AdmissionPredicates) sdktools.Tool {
	wrapped := inner
	if pred.ApprovalGate != nil {
		wrapped = &approvalGatedToolAdapter{
			inner:           wrapped,
			cliName:         cliTool.Name(),
			gate:            pred.ApprovalGate,
			standing:        pred.ApprovalStanding,
			policy:          pred.ApprovalPolicy,
			emitPending:     pred.EmitPending,
			getCapabilities: capabilitiesFor(cliTool),
		}
	}
	if pred.StagedMessage != nil || pred.UnadmittedHandler != nil {
		wrapped = &admissionCheckedToolAdapter{
			inner:      wrapped,
			cliName:    cliTool.Name(),
			staged:     pred.StagedMessage,
			unadmitted: pred.UnadmittedHandler,
		}
	}
	return wrapped
}

// WrapRegistryWithAdmission wraps each tool in sdkReg with the
// admission and approval predicates corresponding to cliReg.
func WrapRegistryWithAdmission(sdkReg *sdktools.Registry, cliReg *tools.Registry, pred AdmissionPredicates) error {
	if sdkReg == nil || (pred.StagedMessage == nil && pred.UnadmittedHandler == nil && pred.ApprovalGate == nil) {
		return nil
	}
	for _, t := range sdkReg.Tools() {
		name := t.Name()
		var cliTool tools.Tool
		if cliReg != nil {
			if ct, ok := cliReg.Get(name); ok {
				cliTool = ct
			}
		}
		if cliTool == nil {
			continue
		}
		wrapped := WrapToolWithAdmission(t, cliTool, pred)
		sdkReg.Remove(name)
		if err := sdkReg.Add(wrapped); err != nil {
			_ = sdkReg.Add(t)
			return fmt.Errorf("sdkadapter: wrap tool %q in SDK registry: %w", name, err)
		}
	}
	return nil
}

// ConvertToolRegistryWithAdmission converts a CLI registry to an SDK
// registry, wrapping each tool with the admission predicates. A nil
// pred produces the same result as ConvertToolRegistry. The check
// runs at call time so UnadmittedHandler's auto-stage side effect
// fires only when the model actually invokes the unadmitted tool.
//
// When pred.ApprovalGate is non-nil, an approval layer is added
// OUTSIDE the admission layer: the admission checks (staged /
// unadmitted) run first; if those pass, the approval gate runs
// before the inner CLI tool. Layering order matters - a staged
// tool never reaches the approval gate.
// regOpts (for example sdktools.WithDefaultRunTimeout) are forwarded
// verbatim to the SDK's New, so the registry-wide run-timeout backstop
// is the caller's choice rather than the SDK's hardcoded default.
func ConvertToolRegistryWithAdmission(cliReg *tools.Registry, pred AdmissionPredicates, regOpts ...sdktools.Option) (*sdktools.Registry, error) {
	if cliReg == nil {
		return nil, nil
	}
	if pred.StagedMessage == nil && pred.UnadmittedHandler == nil && pred.ApprovalGate == nil {
		return ConvertToolRegistry(cliReg, regOpts...)
	}
	sdkReg := sdktools.New(regOpts...)
	for _, t := range cliReg.List() {
		inner, err := newSDKToolAdapter(t)
		if err != nil {
			return nil, err
		}
		wrapped := WrapToolWithAdmission(inner, t, pred)
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

// Compile-time assertions: the adapter satisfies the SDK interfaces.
var _ sdktools.Tool = (*sdkToolAdapter)(nil)
var _ sdktools.SchemaTool = (*sdkToolAdapter)(nil)
var _ sdktools.ProfiledTool = (*sdkToolAdapter)(nil)

// ExecutionProfile publishes the CLI tool's Capability as the SDK
// ExecutionProfile, so the SDK's run-timeout backstop honors a
// declared Capability.Timeout (dispatch_tasks' 12h orchestration
// budget) instead of killing the run at its own registry default. A
// CLI tool without a Capability yields the zero profile, whose zero
// Timeout means "undeclared" in the SDK's resolution table: the
// registry-wide default (from CLI config) applies. The nil args
// mirror capableToolBridge: the SDK interface is static, so the
// bridge reads the zero-payload capability shape.
func (s *sdkToolAdapter) ExecutionProfile() sdktools.ExecutionProfile {
	if capable, ok := s.cli.(tools.CapableTool); ok {
		return CapabilityToExecutionProfile(capable.Capability(nil))
	}
	return sdktools.ExecutionProfile{}
}

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
	return &sdkToolAdapter{cli: t, schema: relaxTopLevelAdditionalProperties(schema)}, nil
}

// relaxTopLevelAdditionalProperties strips a top-level
// "additionalProperties": false from a tool's schema. The legacy CLI
// never validated call arguments against the schema - the tool's own
// Execute owns argument validation and the dispatcher renders its
// failure as the bounded JSON error envelope. The SDK loop compiles
// and enforces the published schema, so without this relaxation an
// extra model-supplied field is rejected by the loop before the tool
// runs, replacing the CLI's JSON envelope with the SDK's
// "[tool-error]" notice. Nested keywords pass through unchanged.
func relaxTopLevelAdditionalProperties(schema []byte) []byte {
	if !bytes.Contains(schema, []byte(`"additionalProperties"`)) {
		return schema
	}
	var doc map[string]any
	if err := json.Unmarshal(schema, &doc); err != nil {
		return schema
	}
	if flag, ok := doc["additionalProperties"]; !ok || flag != false {
		return schema
	}
	delete(doc, "additionalProperties")
	out, err := json.Marshal(doc)
	if err != nil {
		return schema
	}
	return out
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
//
// regOpts (for example sdktools.WithDefaultRunTimeout) are forwarded
// verbatim to the SDK's New. Without an explicit run-timeout option the
// SDK bounds every no-profile tool at its hardcoded DefaultRunTimeout
// (10 minutes); callers that arm their own per-call deadlines pass
// sdktools.WithDefaultRunTimeout(sdktools.TimeoutNone) to keep the SDK
// backstop from being tighter than their declared budgets.
func ConvertToolRegistry(cliReg *tools.Registry, regOpts ...sdktools.Option) (*sdktools.Registry, error) {
	if cliReg == nil {
		return nil, nil
	}
	sdkReg := sdktools.New(regOpts...)
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
