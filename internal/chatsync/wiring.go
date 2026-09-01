package chatsync

import (
	"context"
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

// DefaultAuthorUserIDProvider resolves the logged-in CLI session's own
// authenticated principal id into an AuthorUserIDProvider, or nil when there
// is no local session.
//
// Unlike DefaultTokenProvider, the closure this returns is NOT free to call:
// every invocation hits POST-free but network-bound /v1/auth/me
// (miviaauth.Service.Whoami), with no local cache of the identity to fall
// back on. InputPoller.resolveAuthorUserID calls it at most once per poller
// lifetime and caches the result, specifically so this cost is paid once,
// lazily, only when a remote input actually needs verifying - never
// unconditionally at session-attach time, which would put a real network
// call on every chat-sync session this process ever opens.
func DefaultAuthorUserIDProvider() AuthorUserIDProvider {
	if !miviaauth.HasDefaultSession() {
		return nil
	}
	svc, err := miviaauth.DefaultService()
	if err != nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		who, err := svc.Whoami(ctx)
		if err != nil {
			return "", err
		}
		return who.Identity.ID, nil
	}
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
