package ledger

import "github.com/MiviaLabs/mivia-agent/internal/sdkadapter"

// The canonical content-reference minter lives in internal/sdkadapter.
// These re-exports keep the ledger API stable for callers that already
// depend on this package: every name points at the bridge's
// implementation, so there is still exactly one place where a
// "ref:<kind>:<digest>" is produced. The invariant: "A reference
// handed to the model resolves, or it is not handed to the model."
//
// See internal/sdkadapter/ref.go for the minter.

// Reference kinds for content-addressed task results and agent messages.
const (
	RefKindOutput    = sdkadapter.KindOutput
	RefKindError     = sdkadapter.KindError
	RefKindMessage   = sdkadapter.KindMessage
	RefKindToolCalls = sdkadapter.KindToolCalls
	RefKindNote      = sdkadapter.KindNote
)

// ErrMalformedReference reports a reference that is not in canonical form.
var ErrMalformedReference = sdkadapter.ErrMalformedReference

// Reference returns the canonical reference for data: "ref:<kind>:<64 hex>".
// It returns "" for empty data or an unrecognised kind.
func Reference(kind string, data []byte) string {
	return sdkadapter.Mint(kind, data)
}

// ParseReference splits a canonical reference into its kind and hex
// digest, returning ErrMalformedReference for any other shape.
func ParseReference(ref string) (kind, digest string, err error) {
	return sdkadapter.Parse(ref)
}
