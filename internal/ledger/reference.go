package ledger

import "github.com/MiviaLabs/mivia-agent/internal/contentref"

// The canonical content-reference minter lives in internal/contentref, a
// stdlib-only leaf package, because internal/runtime must reach it too and
// cannot import this package without an import cycle. These are delegations,
// not a second implementation: no reference format string appears here, so
// there remains exactly one place where a "ref:<kind>:<digest>" is produced.
// The invariant: "A reference handed to the model resolves, or it is not
// handed to the model."

// Reference kinds for content-addressed task results and agent messages.
const (
	RefKindOutput  = contentref.KindOutput
	RefKindError   = contentref.KindError
	RefKindMessage = contentref.KindMessage
)

// ErrMalformedReference reports a reference that is not in canonical form.
var ErrMalformedReference = contentref.ErrMalformed

// Reference returns the canonical reference for data: "ref:<kind>:<64 hex>".
// It returns "" for empty data or an unrecognised kind.
func Reference(kind string, data []byte) string {
	return contentref.Reference(kind, data)
}

// ParseReference splits a canonical reference into its kind and hex digest,
// returning ErrMalformedReference for any other shape.
func ParseReference(ref string) (kind, digest string, err error) {
	return contentref.Parse(ref)
}
