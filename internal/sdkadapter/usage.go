// Package sdkadapter — usage bridge.
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
	sdkusage "github.com/MiviaLabs/mivia-ai-sdk/usage"
)

// Accumulator re-exports the SDK's per-session usage.Accumulator. See
// the package doc for why the bridge is a type alias.
type Accumulator = sdkusage.Accumulator

// NewAccumulator returns an empty SDK Accumulator ready to record.
func NewAccumulator() *Accumulator { return sdkusage.New() }

// WrapCompleter returns a provider.Completer that records every
// completed Chat turn's usage under sessionID in a.
func WrapCompleter(sessionID string, a *Accumulator, c provider.Completer) (provider.Completer, error) {
	return sdkusage.WrapCompleter(sessionID, a, c)
}

// Re-exported sentinels so CLI callers can errors.Is against
// sdkadapter.ErrBlankSessionID without an extra SDK import.
var (
	ErrBlankSessionID = sdkusage.ErrBlankSessionID
	ErrNilAccumulator = sdkusage.ErrNilAccumulator
	ErrNilCompleter   = sdkusage.ErrNilCompleter
)
