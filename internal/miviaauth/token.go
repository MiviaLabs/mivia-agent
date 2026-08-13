// token.go holds the local CLI auth token model shared by the mivia
// login/refresh flow. The package doc lives on openapi_types.gen.go's
// generated header; this file intentionally does not repeat a "Package
// miviaauth" comment, since go/doc concatenates every such comment across
// a package's files and a second one here would just create noise.
package miviaauth

import "time"

// Token is the local CLI credential issued after a successful login.
type Token struct {
	Bearer         string
	ExpiresAt      time.Time
	OrganizationID string
	Role           string
}

// Expired reports whether the token is expired at now (now >= ExpiresAt).
func (t Token) Expired(now time.Time) bool {
	return !now.Before(t.ExpiresAt)
}

// NeedsRefresh reports whether the token is expired, or expires within skew
// of now.
func (t Token) NeedsRefresh(now time.Time, skew time.Duration) bool {
	return t.ExpiresAt.Sub(now) <= skew
}
