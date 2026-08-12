package config

import (
	"net/url"
	"strings"
)

// IsOllamaLoopback reports whether raw is an absolute http(s) URL whose
// hostname is a loopback literal (127.0.0.1, ::1, localhost), with no
// userinfo and no fragment. The hostname is matched as a literal string
// only — no DNS resolution, no CIDR, no IP normalization — so any other
// host text fails closed. localhost is trusted as loopback per the locked
// plan; environments where localhost does not resolve to loopback should
// use 127.0.0.1. The provider layer complements this literal predicate with
// a construction-time resolution check and a pinned dial
// (newLoopbackDialContext) so keyless traffic can only reach a verified
// loopback address; a localhost that resolves to a non-loopback address
// fails closed.
func IsOllamaLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "" {
		return false
	}
	if u.User != nil {
		return false
	}
	if u.Fragment != "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}
