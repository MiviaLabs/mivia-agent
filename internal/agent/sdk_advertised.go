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

// applyAdvertisedTools REPLACES the converted request's
// registry-derived tool array with the run's pinned advertised
// snapshot when one exists (the legacy initialToolSpecs contract:
// request 0 carries the host-pinned union, deferred tools like a
// not-yet-registered "grep" included, and each surface rotation's
// non-nil ToolSpecs replace it). Replace, not append: the SDK request
// already carries the registry definitions, so appending would
// double-advertise every registered tool. A nil snapshot (no seed, no
// rotation) leaves the request's registry-derived tools untouched -
// the subagent and workflow-engine paths.
//
// Recovery-request safety (reviewer amendment B): the SDK's
// prompt-too-long recovery (agentloop/compaction.go
// recoverPromptTooLong) re-sends its retry through the same Completer
// Chat with Tools: l.defs, so a recovery request is NOT distinguishable
// from a run request by request shape - both carry the loop's defs.
// The gate is structural instead: recoverPromptTooLong runs only when
// Options.Window is non-nil (agentloop/run.go runChat), and the host
// NEVER wires a Window (sdk-backend-field-mapping.md; pinned by
// TestBuildAgentLoopOptions_NeverWiresWindow), so no recovery request
// can reach this completer on the SDK path. The host's own
// prompt-too-long retry (runSDKPromptTooLongRecoverable) rebuilds a
// fresh loop whose iteration-1 request is a normal run request that
// legitimately carries the union - the same shape the legacy retry's
// requests carry, because the legacy loop also advertises the pinned
// union on every step's request.
func applyAdvertisedTools(req provider.Request, advertised func() []provider.ToolSpec) provider.Request {
	if advertised == nil {
		return req
	}
	if specs := advertised(); specs != nil {
		req.Tools = specs
	}
	return req
}
