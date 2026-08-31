package miviaauth

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// DefaultService builds the Service that speaks to the configured API root
// and reads the standard local token file (~/.mivia/auth.json).
//
// It exists so every caller that needs "the logged-in CLI session" resolves
// the same server URL and the same file. A second Service over a second path,
// or a hand-rolled token cache, would be a second refresher of a one-time-use
// refresh token, which is what lock.go exists to prevent.
func DefaultService() (*Service, error) {
	client, err := NewClient(ServerURLFromEnv())
	if err != nil {
		return nil, fmt.Errorf("miviaauth: default service: %w", err)
	}
	path := config.UserAuthPath()
	if path == "" {
		return nil, fmt.Errorf("miviaauth: default service: no home directory for the token file")
	}
	return NewService(client, path), nil
}

// HasDefaultSession reports whether a usable local CLI session exists at the
// standard token path, WITHOUT touching, refreshing, or deleting it.
//
// It is the "am I logged in" question a caller asks before deciding to do
// something on the user's behalf. It is deliberately read-only: the obvious
// alternative, calling Service.Ensure, spends a one-time-use refresh token and
// deletes the stored session on any load failure, so using it as a probe would
// log the user out as a side effect of asking.
//
// The RefreshToken check mirrors Service.resolveToken's own rule: a file
// without one was written before the /v1 contract, and its bearer cannot
// authenticate against this API at all, so it is not a session. Expiry is NOT
// checked, for the same reason resolveToken does not treat it as fatal: an
// expired bearer with a live refresh token is a session that renews itself.
func HasDefaultSession() bool {
	path := config.UserAuthPath()
	if path == "" {
		return false
	}
	tok, err := Load(path)
	if err != nil {
		return false
	}
	return tok.RefreshToken != ""
}
