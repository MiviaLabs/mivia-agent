//go:build livechat

// Live contract probe for the deployed /v1/chat-sessions surface.
//
// Tagged rather than env-gated for the same reason as
// internal/miviaauth/live_smoke_test.go: AGENTS.md forbids running a live e2e
// without an explicit ask, and a build tag cannot fire by accident the way a
// skipped test can when an env var happens to be set. Nothing here skips - if
// you set the tag you asked for a live run, so missing credentials fail.
//
// Run with `make live-chat-smoke`.
//
// This file holds the harness: credentials, the bearer, the HTTP plumbing, and
// wire structs mirroring the API's DTOs. The structs are deliberately local to
// the test. The CLI client does not exist yet and its design is still open, so
// pinning the SERVER's shape here proves the contract without freezing any
// decision about how the client should be built.

package chatsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

const (
	envBaseURL  = "MIVIA_LIVE_API_BASE_URL"
	envEmail    = "MIVIA_LIVE_EMAIL"
	envPassword = "MIVIA_LIVE_PASSWORD"
)

// liveTimeout bounds a whole test. It is generous because one probe parks a
// 25-second long poll on purpose.
const liveTimeout = 120 * time.Second

// ---------------------------------------------------------------------------
// Wire types. Field names and nullability mirror the API's response DTOs.
// Timestamps stay strings: the format is part of the contract, so the probe
// reports it rather than failing inside a decoder.
// ---------------------------------------------------------------------------

type (
	session       = Session
	eventItem     = EventItem
	storedEvent   = StoredEvent
	appendResult  = AppendResult
	sessionInput  = SessionInput
	nextInput     = NextInput
	errorEnvelope = ErrorEnvelope
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type api struct {
	t       *testing.T
	baseURL string
	bearer  string
	client  *http.Client
}

// newAPI logs in with the live credentials and returns a probe bound to a real
// bearer. It reuses internal/miviaauth rather than re-implementing login, so
// the probe exercises the same auth path the CLI will.
func newAPI(t *testing.T, ctx context.Context) *api {
	t.Helper()
	var missing []string
	get := func(name string) string {
		v := os.Getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	baseURL, email, password := get(envBaseURL), get(envEmail), get(envPassword)
	if len(missing) > 0 {
		t.Fatalf("live chat probe needs %v in the environment; use a throwaway account", missing)
	}

	authClient, err := miviaauth.NewClient(baseURL)
	if err != nil {
		t.Fatalf("NewClient(%q): %v", baseURL, err)
	}
	tok, err := authClient.Login(ctx, email, []byte(password))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return &api{
		t:       t,
		baseURL: strings.TrimRight(baseURL, "/"),
		bearer:  tok.Bearer,
		// No client-level timeout: one probe parks a 25s long poll, and the
		// per-request context is the real bound.
		client: &http.Client{},
	}
}

func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	t.Cleanup(cancel)
	return ctx
}

// call issues a request and returns the status and raw body. It never fails
// the test: probes assert on the status themselves, including the error ones.
func (a *api) call(ctx context.Context, method, path string, body any) (int, []byte) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("encode %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, reader)
	if err != nil {
		a.t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		a.t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

// expect asserts the status and decodes the body into out when out is non-nil.
func (a *api) expect(ctx context.Context, method, path string, body any, want int, out any) {
	a.t.Helper()
	status, raw := a.call(ctx, method, path, body)
	if status != want {
		a.t.Fatalf("%s %s = %d, want %d; body: %s", method, path, status, want, truncate(raw))
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(raw, out); err != nil {
		a.t.Fatalf("decode %s %s: %v; body: %s", method, path, err, truncate(raw))
	}
}

// decodeInto decodes a body captured by call, for probes that must inspect a
// response they did not assert a status on.
func (a *api) decodeInto(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}

// decodeError reads the API's error envelope, reporting the raw body when the
// response is not in that shape at all.
func (a *api) decodeError(raw []byte) errorEnvelope {
	a.t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		a.t.Fatalf("error body is not the API envelope: %v; body: %s", err, truncate(raw))
	}
	return env
}

// createSession registers a session and ends it when the test finishes. There
// is no DELETE endpoint, so ended rows stay in the dev database; the title
// carries a marker so they are identifiable.
func (a *api) createSession(ctx context.Context, name string) session {
	a.t.Helper()
	var s session
	a.expect(ctx, http.MethodPost, "/v1/chat-sessions", map[string]any{
		"title":     "mivia live probe: " + name,
		"cwdLabel":  "live-probe",
		"hostLabel": "live-probe",
	}, http.StatusCreated, &s)

	a.t.Cleanup(func() {
		// Best effort: the probe must not fail because cleanup could not run.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = a.call(cleanupCtx, http.MethodPost, "/v1/chat-sessions/"+s.ID+"/end", nil)
	})
	return s
}

func (a *api) appendEvents(ctx context.Context, id string, events []eventItem, want int) (appendResult, []byte) {
	a.t.Helper()
	status, raw := a.call(ctx, http.MethodPost, "/v1/chat-sessions/"+id+"/events",
		map[string]any{"events": events})
	if status != want {
		a.t.Fatalf("append events = %d, want %d; body: %s", status, want, truncate(raw))
	}
	var result appendResult
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &result); err != nil {
			a.t.Fatalf("decode append result: %v; body: %s", err, truncate(raw))
		}
	}
	return result, raw
}

func truncate(raw []byte) string {
	const limit = 600
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + fmt.Sprintf("... (%d bytes total)", len(raw))
}
