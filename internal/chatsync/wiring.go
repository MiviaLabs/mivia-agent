package chatsync

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	return ResolveEndpoint(configured).URL
}

// Endpoint is where sync would upload, why, and whether it could log in.
type Endpoint struct {
	URL string
	// Source names what supplied URL: "[sync] api_url", the env var with the
	// file or process that set it, or "default".
	Source string
	// TokenPresent reports whether a saved login exists. Sync activates only
	// when it does, so a probe without one proves nothing about sync.
	TokenPresent bool
}

// ResolveEndpoint resolves the sync API root the same way OpenSession will
// use it, and says where the value came from. It is the ONE resolver: a
// diagnostic that re-derived the chain would be a second place to get it
// wrong, which is the failure the diagnostic exists to catch.
func ResolveEndpoint(configured string) Endpoint {
	e := Endpoint{TokenPresent: miviaauth.HasDefaultSession()}
	if trimmed := strings.TrimSpace(configured); trimmed != "" {
		e.URL, e.Source = trimmed, "[sync] api_url"
		return e
	}
	e.URL, e.Source = miviaauth.ResolveServerURL()
	return e
}

// Describe renders the endpoint for a notice: the URL and, in parentheses,
// what supplied it.
func (e Endpoint) Describe() string {
	return e.URL + " (" + e.Source + ")"
}

// probeTimeout bounds ProbeEndpoint. A diagnostic must return; a dead host
// that hangs the doctor command is worse than one it reports as unreachable.
const probeTimeout = 3 * time.Second

// ProbeEndpoint makes one bounded, unauthenticated request to the API's
// version-neutral /health route and reports what came back. Any HTTP answer
// means the host is reachable; the status code is reported so a URL that
// points at something other than the mivia API (a web app answering 200 to
// everything, a proxy answering 404) can be told apart from the real one.
func ProbeEndpoint(ctx context.Context, baseURL string) (reachable bool, detail string) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return false, "invalid url: " + err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "unreachable: " + err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return true, "reachable (HTTP " + strconv.Itoa(resp.StatusCode) + ")"
}
