package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// toolSchemaDigest fingerprints the advertised tool-schema array (the pinned
// binding-lifetime snapshot, not the live execution registry - plan
// tools-advertising/01: admission changes execution authority, never the
// wire-advertised tools[]). json.Marshal of the map slice sorts object keys
// deterministically, so the digest is stable for an equal snapshot. The slice
// ORDER is preserved, which is exactly what the wire sends, so an order change
// also changes the digest: INV-68-1 forbids a wire-affecting input absent from
// the identity. Computed once per identity-capture trigger, never per turn
// (INV-68-8).
func toolSchemaDigest(specs []provider.ToolSpec) string {
	sum := sha256.New()
	if len(specs) == 0 {
		sum.Write([]byte("[]"))
	} else if raw, err := json.Marshal(specs); err == nil {
		sum.Write(raw)
	} else {
		// specs is built from plain maps and cannot fail to marshal; fall
		// back to the empty-array digest so the identity stays deterministic
		// rather than carrying a partial hash.
		sum.Write([]byte("[]"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// systemPromptDigest fingerprints the system-prompt text that reaches the
// wire. Computed once per identity-capture trigger, never per turn (INV-68-8).
// The empty prompt hashes deterministically like any other value.
func systemPromptDigest(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}
