// Usage bridge.
//
// Accumulator re-exports the SDK's per-session usage.Accumulator. The
// CLI reaches the bridge, never the SDK directly, so the SDK dependency
// stays inside internal/sdkadapter. The bridge is a type alias on
// purpose: local code that has *sdkusage.Accumulator via the SDK's own
// wiring (B.2 #8, when it lands) shares the same pointer the bridge
// returns, and methods dispatched through either name reach the same
// Record/Total/Reset implementation without a wrapper allocation.

package sdkadapter

import (
	"github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// Accumulator re-exports the SDK's per-session provider.Accumulator
// (folded in from the former usage package). See the package doc for
// why the bridge is a type alias.
type Accumulator = provider.Accumulator

// NewAccumulator returns an empty SDK Accumulator ready to record.
func NewAccumulator() *Accumulator { return provider.NewAccumulator() }
