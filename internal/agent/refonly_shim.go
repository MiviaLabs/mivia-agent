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
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// refOnlyShim wraps an SDK tool with the per-call ref-notice
// shim. The shim's Run invokes the inner tool; if the result body
// clears Floor and the tool is named in Names, Run calls the CLI's
// *remainder.Spool to mint a ref and returns the same ref-notice
// the legacy CLI shape_batch ref-only tier produces. A nil Spool
// or a non-positive Floor makes the shim a transparent pass-through.
type refOnlyShim struct {
	inner     sdktools.Tool
	spool     *remainder.Spool
	names     []string
	floor     int
	principal string
}

var _ sdktools.Tool = (*refOnlyShim)(nil)

func (r *refOnlyShim) Name() string { return r.inner.Name() }

func (r *refOnlyShim) Run(ctx context.Context, in sdktools.InOut) (sdktools.Out, error) {
	body, err := r.inner.Run(ctx, in)
	if err != nil {
		return body, err
	}
	if r.spool == nil || r.floor <= 0 || r.principal == "" {
		return body, nil
	}
	name := r.inner.Name()
	if !slices.Contains(r.names, name) {
		return body, nil
	}
	s, ok := body.Value.(string)
	if !ok || len(s) < r.floor {
		return body, nil
	}
	ref := r.spool.Spool(ctx, r.principal, []byte(s))
	if ref == "" {
		// Plain notice when the spool cannot mint (nil store, empty
		// principal, or write failure). Mirrors refOnlyTier:36-43.
		return sdktools.Out{Value: fmt.Sprintf("[tool result for %s elided; original ~%s]", name, refOnlySizeLabel(len(s)))}, nil
	}
	return sdktools.Out{Value: fmt.Sprintf("[tool result for %s elided to a remainder ref (original ~%s): %s — use read_output to fetch the full body]", name, refOnlySizeLabel(len(s)), ref)}, nil
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
// existing wrapped form. A nil spool or empty names returns the
// registry unchanged.
func applyRefOnlyShim(sdkReg *sdktools.Registry, names []string, spool *remainder.Spool, floor int, principal string) {
	if sdkReg == nil || len(names) == 0 || spool == nil || floor <= 0 || principal == "" {
		return
	}
	for _, name := range names {
		t, ok := sdkReg.Get(name)
		if !ok {
			continue
		}
		// Replace by re-adding under a fresh shim. The SDK's
		// Registry exposes Remove, so the swap is two operations.
		sdkReg.Remove(name)
		wrapped := &refOnlyShim{
			inner:     t,
			spool:     spool,
			names:     names,
			floor:     floor,
			principal: principal,
		}
		if err := sdkReg.Add(wrapped); err != nil {
			// Restore the unwrapped tool so the registry stays
			// usable; the shim is best-effort.
			_ = sdkReg.Add(t)
		}
	}
}
