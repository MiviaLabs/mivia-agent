package sdkadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider/reasoning"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

// LevelToReasoningEffort maps the CLI's provider-neutral reasoning Level
// (eight values: off, minimal, low, medium, high, xhigh, max, auto) onto the
// SDK's ReasoningEffort (four values: none, low, medium, high). The empty
// Level maps to the empty SDK effort, which is the SDK's "send no reasoning
// field" reading.
//
// Returns (effort, true) for the four levels the SDK has a constant for and
// for the empty Level; (empty, false) for levels that have no SDK
// analogue (minimal, xhigh, max, auto). The boolean lets the caller
// distinguish
// "the SDK has no surface for this" from "the user did not pick a level":
// a (false) result is a refused conversion, not a default.
//
// The mapping is product-specific: it encodes CLI's decisions about which
// CLI configs survive the cutover to the SDK wire shape. It lives here
// (not in internal/provider/reasoning) because internal/sdkadapter is the only
// package permitted to import both CLI and SDK shapes; placing the bridge
// in internal/provider/reasoning would force the SDK dependency onto every caller
// of that package and would break internal/provider/reasoning's deliberate
// stdlib-only contract (reasoning.go:5-7).
func LevelToReasoningEffort(l reasoning.Level) (sdkshape.ReasoningEffort, bool) {
	switch l {
	case "":
		return "", true
	case reasoning.Off:
		return sdkshape.ReasoningEffortNone, true
	case reasoning.Low:
		return sdkshape.ReasoningEffortLow, true
	case reasoning.Medium:
		return sdkshape.ReasoningEffortMedium, true
	case reasoning.High:
		return sdkshape.ReasoningEffortHigh, true
	case reasoning.Minimal, reasoning.XHigh, reasoning.Max, reasoning.Auto:
		return "", false
	default:
		return "", false
	}
}
