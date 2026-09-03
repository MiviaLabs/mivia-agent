// Package agent - pinned advertised-tool-union carrier for the SDK
// backend. See sdk_advertised_test.go for the pinned contracts.

package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// seedSDKTurnState installs the run's initial surface and advertised
// values: the caller's Dispatcher/RemainderSpool (per-call shim reads)
// and, when the host pinned one, the AdvertisedToolSpecs snapshot that
// carries the whole advertised union onto request 0 (the legacy
// initialToolSpecs contract; see applyAdvertisedTools below).
func seedSDKTurnState(turn *sdkTurnState, opts Options) {
	turn.seedSurface(opts.Dispatcher, opts.RemainderSpool)
	turn.setAdvertised(opts.AdvertisedToolSpecs)
}

// applyAdvertisedTools replaces the request's registry-derived tool array with
// the run's pinned advertised snapshot when non-nil (legacy initialToolSpecs
// contract: request 0 carries pinned union including deferred tools; surface
// rotations replace it). Replace, not append: appending would double-advertise
// registered tools. Nil snapshot leaves registry tools untouched (subagent and
// workflow paths).
//
// Recovery-request safety: SDK prompt-too-long recovery (recoverPromptTooLong)
// re-sends via Completer Chat with Tools: l.defs, making it indistinguishable
// from a run request by shape. The gate is structural: recoverPromptTooLong runs
// only when Options.Window != nil (runChat), and the host never wires a Window
// (pinned by TestBuildAgentLoopOptions_NeverWiresWindow). The host's own retry
// (runSDKPromptTooLongRecoverable) rebuilds a fresh loop whose normal run request
// carries the union, matching legacy loop behavior.
func applyAdvertisedTools(req provider.Request, advertised func() []provider.ToolSpec) provider.Request {
	if advertised == nil {
		return req
	}
	if specs := advertised(); specs != nil {
		req.Tools = specs
	}
	return req
}
