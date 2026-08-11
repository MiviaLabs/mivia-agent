package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// toolSchemaDigest fingerprints the registry's OpenAI tool-schema array.
// json.Marshal of the map slice sorts object keys deterministically, so the
// digest is stable for an equal registry. The slice ORDER is preserved, which
// is exactly what the wire sends (tool order is deterministic per binding via
// ScopedRegistry - plan 51/05 B6), so an order change also changes the digest:
// INV-68-1 forbids a wire-affecting input absent from the identity. Computed
// once per identity-capture trigger, never per turn (INV-68-8).
func toolSchemaDigest(reg *tools.Registry) string {
	sum := sha256.New()
	if reg == nil {
		sum.Write([]byte("[]"))
	} else if raw, err := json.Marshal(reg.OpenAITools()); err == nil {
		sum.Write(raw)
	} else {
		// OpenAITools is built from plain maps and cannot fail to marshal;
		// fall back to the empty-array digest so the identity stays
		// deterministic rather than carrying a partial hash.
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
