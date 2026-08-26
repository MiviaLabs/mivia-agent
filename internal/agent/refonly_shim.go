// Package agent - ref-only tool shim for the SDK backend.
//
// The legacy CLI's refOnlyTier (internal/agent/shape_batch_refonly.go:25-45)
// is a per-result shaper that runs inside the batch shaper and
// spools a tool result to the CLI's *remainder.Spool when the tool
// is in Options.RefOnlyTools and the rendered body clears
// BatchDegradeFloorBytes. On the SDK path the SDK's own
// spool.SpoolTool is unreachable (it requires WithPrincipal on ctx,
// which no SDK call site attaches — see
// mivia-ai-sdk/spool/tool.go:38-42). The shim here is the
// agent-side substitute: each tool named in RefOnlyTools is wrapped
// with this per-call wrapper after the SDK registry conversion.
//
// The shim's Run calls the inner SDK tool, then if the body is a
// string over the floor and the wrapper is on the named list, it
// calls the CLI's *remainder.Spool.Spool directly with the
// configured principal and returns the same ref-notice the legacy
// shape_batch produces. The spool type is
// internal/remainder.Spool, which lives downstream of internal/sdkadapter
// (it imports sdkadapter for sdkadapter.Mint), so the shim type
// itself belongs in the consumer package that bridges the two —
// not in sdkadapter, which would create an import cycle.
package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// refOnlyShim wraps an SDK tool with the per-call ref-notice
// shim. The shim's Run invokes the inner tool; if the result body
// clears Floor and the tool is named in Names, Run calls the CLI's
// *remainder.Spool to mint a ref and returns the same ref-notice
// the legacy CLI shape_batch ref-only tier produces. A nil Spool
// or a non-positive Floor makes the shim a transparent pass-through.
type refOnlyShim struct {
	inner sdktools.Tool
	// schema is the same tool's SchemaTool face, asserted at wrap
	// time; converter products always implement it.
	schema    sdktools.SchemaTool
	spool     *remainder.Spool
	names     []string
	floor     int
	principal string
	// ephemeral marks a CLI tool implementing tools.EphemeralResultTool:
	// its body must never be spooled durably (mirrors refOnlyTier's
	// p.ephemeral skip at shape_batch_refonly.go:26-27).
	ephemeral bool
	// turn is the run's turn state; a minted ref-notice overwrites the
	// recorded tool-event outcome body so tool_end matches the
	// post-shaping body the model sees (sdk_tool_events.go).
	turn *sdkTurnState
}

var _ sdktools.Tool = (*refOnlyShim)(nil)
var _ sdktools.ProfiledTool = (*refOnlyShim)(nil)

// ExecutionProfile forwards the inner tool's profile: the SDK's
// run-timeout resolver consults only the outermost registered value,
// so every shim layer forwards explicitly (see dispatcherShim).
func (s *refOnlyShim) ExecutionProfile() sdktools.ExecutionProfile {
	return sdktools.ExecutionProfileOf(s.inner)
}

var _ sdktools.SchemaTool = (*refOnlyShim)(nil)

func (r *refOnlyShim) Name() string { return r.inner.Name() }

// ParameterSchema and DecodeArguments delegate to the inner tool: the
// SDK's Definitions skips any tool that does not implement SchemaTool,
// so without the delegation a ref-only tool silently vanishes from the
// offered set.
func (r *refOnlyShim) ParameterSchema() []byte { return r.schema.ParameterSchema() }

func (r *refOnlyShim) DecodeArguments(raw []byte) (sdktools.InOut, error) {
	return r.schema.DecodeArguments(raw)
}

func (r *refOnlyShim) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	body, err := r.inner.Run(ctx, in)
	if err != nil {
		return body, err
	}
	if r.spool == nil || r.floor <= 0 || r.principal == "" {
		return body, nil
	}
	name := r.inner.Name()
	if !slices.Contains(r.names, name) || r.ephemeral {
		return body, nil
	}
	s, ok := body.Value.(string)
	if !ok || len(s) < r.floor {
		return body, nil
	}
	callKey := toolCallKeyFromContext(ctx, name)
	ref := r.spool.Spool(ctx, r.principal, []byte(s))
	if ref == "" {
		// Plain notice when the spool cannot mint (nil store, empty
		// principal, or write failure). Mirrors refOnlyTier:36-43.
		notice := fmt.Sprintf("[tool result for %s elided; original ~%s]", name, refOnlySizeLabel(len(s)))
		if r.turn != nil && callKey != "" {
			r.turn.overwriteToolOutcomeBody(callKey, notice)
		}
		return sdktools.Out{Value: notice}, nil
	}
	notice := fmt.Sprintf("[tool result for %s elided to a remainder ref (original ~%s): %s — use read_output to fetch the full body]", name, refOnlySizeLabel(len(s)), ref)
	if r.turn != nil && callKey != "" {
		r.turn.overwriteToolOutcomeBody(callKey, notice)
	}
	return sdktools.Out{Value: notice}, nil
}

// refOnlySizeLabel rounds n up to the next power of two and renders
// it as KiB or MiB (powers of 1024), matching contextmgr's elision
// notices (see internal/agent/shape_batch_refonly.go:52-67 for the
// legacy implementation).
func refOnlySizeLabel(n int) string {
	if n <= 0 {
		return "0 KiB"
	}
	bucket := refOnlyCeilPowerOfTwo(n)
	const (
		kib = 1024
		mib = 1024 * 1024
	)
	if bucket >= mib {
		return fmt.Sprintf("%d MiB", bucket/mib)
	}
	if bucket < kib {
		return "1 KiB"
	}
	return fmt.Sprintf("%d KiB", bucket/kib)
}

func refOnlyCeilPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	maxInt := int(^uint(0) >> 1)
	p := 1
	for p < n {
		if p > maxInt>>1 {
			return p
		}
		p <<= 1
	}
	return p
}

// applyRefOnlyShim post-processes the SDK registry built by
// sdkadapter.ConvertToolRegistryWithAdmission, wrapping each named
// tool with the ref-only shim. Tools not in names keep their
// existing wrapped form. cliReg is the CLI registry the SDK registry
// was converted from; it is consulted (never mutated) to detect
// tools implementing tools.EphemeralResultTool, whose bodies must
// never be spooled durably. A nil spool or empty names returns the
// registry unchanged.
func applyRefOnlyShim(sdkReg *sdktools.Registry, cliReg *tools.Registry, names []string, spool *remainder.Spool, floor int, principal string, turn *sdkTurnState) {
	if sdkReg == nil || len(names) == 0 || spool == nil || floor <= 0 || principal == "" {
		return
	}
	for _, name := range names {
		t, ok := sdkReg.Get(name)
		if !ok {
			continue
		}
		st, ok := t.(sdktools.SchemaTool)
		if !ok {
			// Converter products always implement SchemaTool; a
			// foreign tool cannot be wrapped without dropping it from
			// the offered set, so it keeps its unwrapped form.
			continue
		}
		var ephemeral bool
		if cliReg != nil {
			if cliTool, ok := cliReg.Get(name); ok {
				_, ephemeral = cliTool.(tools.EphemeralResultTool)
			}
		}
		// Replace by re-adding under a fresh shim. The SDK's
		// Registry exposes Remove, so the swap is two operations.
		sdkReg.Remove(name)
		wrapped := &refOnlyShim{
			inner:     t,
			schema:    st,
			spool:     spool,
			names:     names,
			floor:     floor,
			principal: principal,
			ephemeral: ephemeral,
			turn:      turn,
		}
		if err := sdkReg.Add(wrapped); err != nil {
			// Restore the unwrapped tool so the registry stays
			// usable; the shim is best-effort.
			_ = sdkReg.Add(t)
		}
	}
}
