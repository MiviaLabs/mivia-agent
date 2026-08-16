package provider

import (
	"errors"
	"strings"
)

// ErrMaxTokensExceeded is the sentinel wrapped into a provider error when
// max_tokens exceeds the limit the serving route/upstream actually allows.
// llmgateway exposes one model id across upstream routes with different real
// max_output_tokens caps, so a request at the declared cap can be rejected by
// a tighter route even though it is legal for the model id. The caller
// matches this sentinel with errors.Is to re-ask the turn with a halved cap
// instead of failing the step on a rejection it can recover from. The
// provider-layer clamp is the recovery boundary; a binding-time pre-clamp of
// the declared cap is a possible future optimization, not this change.
var ErrMaxTokensExceeded = errors.New("max tokens cap exceeded")

// isMaxTokensCapMessage classifies a provider's error message as a
// max-tokens-cap rejection. It is a classification aid only: the message text
// is read solely to decide whether to attach ErrMaxTokensExceeded and is
// NEVER surfaced in an error string (rule 10: provider content never leaks).
func isMaxTokensCapMessage(msg string) bool {
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "max_tokens") && !strings.Contains(lower, "max tokens") {
		return false
	}
	for _, marker := range []string{
		"exceed", "at most", "greater than", "larger than", "too large", "too big", "allowed limit",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
