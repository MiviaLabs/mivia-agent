package chatsync

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// DefaultTokenProvider resolves the logged-in CLI session into the
// TokenProvider this package requires, or nil when there is no local session.
//
// A nil result means "do not sync", and every wiring site must treat it that
// way: uploading conversation content anonymously is the failure this returns
// nil to avoid. It is also the activation rule's other half - authentication
// is what turns sync on, so the absence of a session is what turns it off,
// silently and with no error.
//
// It lives here, next to NewTokenProvider, because the plain-CLI surface and
// the TUI session pool are separate wiring sites, and a rule honoured by one
// of two sibling call sites is the defect class this repo hits most often.
func DefaultTokenProvider() TokenProvider {
	if !miviaauth.HasDefaultSession() {
		return nil
	}
	svc, err := miviaauth.DefaultService()
	if err != nil {
		return nil
	}
	return NewTokenProvider(svc)
}

// DefaultBaseURL returns configured when it names something, else the mivia
// API root internal/miviaauth already resolves.
//
// Nothing asks a user to configure sync, so an empty api_url is the normal
// case rather than an error. The fallback reads through miviaauth on purpose:
// that package owns where the API lives (DefaultServerURL, overridable with
// MIVIA_API_BASE_URL), and a second copy of the URL here would be a second
// place to change when it moves, and a second place to get a staging or local
// override wrong.
func DefaultBaseURL(configured string) string {
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		return trimmed
	}
	return miviaauth.ServerURLFromEnv()
}
