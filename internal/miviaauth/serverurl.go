package miviaauth

import (
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	sdkenvfile "github.com/MiviaLabs/mivia-ai-sdk/envfile"
)

// apiVersionSegment matches a path segment that is an API version prefix: v1,
// v2, and so on to any number.
//
// It stays this narrow on purpose. A base URL may carry a legitimate path
// prefix, and only a trailing version is the mistake worth refusing. Names like
// v1beta or vnext do not match, because guessing at those would reject a URL
// that could be someone's real API root.
var apiVersionSegment = regexp.MustCompile(`^[vV][0-9]+$`)

// versionedAPIPath reports the cleaned path of rawURL and its final segment
// when that segment is an API version.
//
// This exists because the version prefix belongs to the request paths in
// client.go, not to the configured root, and the URL a person copies out of a
// dashboard or a chat message is the browsable one, which carries the version.
// Such a value produces a doubled prefix and a 404 that explains nothing.
func versionedAPIPath(rawURL string) (path, segment string, ok bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false
	}
	path = strings.TrimRight(parsed.Path, "/")
	if path == "" {
		return "", "", false
	}
	segment = path[strings.LastIndex(path, "/")+1:]
	if !apiVersionSegment.MatchString(segment) {
		return "", "", false
	}
	return path, segment, true
}

// DefaultServerURL is the mivia API root used when MIVIA_API_BASE_URL is
// unset. Override it for local or staging use -- a local API is typically
// http://localhost:3001.
//
// This is the API ROOT only. The /v1 version prefix belongs to the request
// paths (see client.go), so an override never carries a version.
const DefaultServerURL = "https://api.mivia.app"

// serverURLEnvKey is the env var ServerURLFromEnv resolves.
const serverURLEnvKey = "MIVIA_API_BASE_URL"

// ServerURLFromEnv returns MIVIA_API_BASE_URL if set and non-blank, else
// DefaultServerURL. The process environment wins; ./.env and ~/.mivia/.env
// are consulted as a fallback, matching how provider API keys are resolved
// elsewhere in this repo (internal/config/load.go, internal/config.Lookup)
// -- without this, a value set only in ~/.mivia/.env would be silently
// invisible here, since that file is never loaded into the OS environment.
func ServerURLFromEnv() string {
	url, _ := ResolveServerURL()
	return url
}

// ServerURLSourceDefault is the source ResolveServerURL reports when nothing
// overrides DefaultServerURL.
const ServerURLSourceDefault = "default"

// ResolveServerURL is ServerURLFromEnv with its provenance: the URL and a
// short human label naming what supplied it - the process environment, the
// env file it was read from, or the built-in default.
//
// The label exists for the diagnostic surfaces. A user staring at a sync that
// uploads to nowhere needs to know WHICH file to fix, and the URL alone does
// not say.
func ResolveServerURL() (url, source string) {
	if v := strings.TrimSpace(os.Getenv(serverURLEnvKey)); v != "" {
		return v, serverURLEnvKey + " (process env)"
	}
	path, ok := config.FirstExisting(config.DefaultEnvCandidates())
	if !ok {
		return DefaultServerURL, ServerURLSourceDefault
	}
	envMap, err := sdkenvfile.Load(path)
	if err != nil {
		return DefaultServerURL, ServerURLSourceDefault
	}
	if v := strings.TrimSpace(envMap[serverURLEnvKey]); v != "" {
		return v, serverURLEnvKey + " (" + path + ")"
	}
	return DefaultServerURL, ServerURLSourceDefault
}
