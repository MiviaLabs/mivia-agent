package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// TestLoginRequestError_RateLimitedAndGenericFallback pins the two
// loginRequestError branches no other test drives: the 429 rate-limit
// message, and the final generic %w wrap for an error that either is not a
// *miviaauth.StatusError or carries a status code none of the switch cases
// name.
func TestLoginRequestError_RateLimitedAndGenericFallback(t *testing.T) {
	rateLimited := &miviaauth.StatusError{StatusCode: http.StatusTooManyRequests}
	if got := loginRequestError(rateLimited); !strings.Contains(got.Error(), "rate limited") {
		t.Fatalf("loginRequestError(429) = %q, want a rate-limited message", got.Error())
	}

	generic := errors.New("dial tcp: connection refused")
	if got := loginRequestError(generic); !strings.Contains(got.Error(), generic.Error()) {
		t.Fatalf("loginRequestError(generic) = %q, want it to wrap %q", got.Error(), generic.Error())
	}
}
