package miviaauth

import "time"

// Token is the local CLI credential issued after a successful login. It is
// what ~/.mivia/auth.json holds, serialized with these field names.
type Token struct {
	// Bearer is the access token. It is short-lived (one hour) and is what
	// every authenticated request carries.
	Bearer string

	// RefreshToken is the long-lived (30-day) credential that buys a new
	// Bearer. It is ONE-TIME USE: the server rotates it on every refresh and
	// treats a reused value as theft, revoking the whole session. It is also
	// the marker that distinguishes a session file written against the /v1
	// contract from a pre-/v1 one, which carried no refresh token and whose
	// Bearer this API cannot authenticate at all.
	RefreshToken string

	ExpiresAt      time.Time
	OrganizationID string
	Role           string
}

// Expired reports whether the token is expired at now (now >= ExpiresAt).
func (t Token) Expired(now time.Time) bool {
	return !now.Before(t.ExpiresAt)
}

// NeedsRefresh reports whether the token is expired, or expires within skew
// of now. An expired token is included deliberately: under this contract a
// refresh is driven by the stored refresh token, not by the expiring bearer,
// so expiry is a reason to refresh rather than a reason to give up.
func (t Token) NeedsRefresh(now time.Time, skew time.Duration) bool {
	return t.ExpiresAt.Sub(now) <= skew
}
