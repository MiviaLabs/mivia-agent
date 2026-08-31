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
