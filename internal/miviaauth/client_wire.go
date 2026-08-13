package miviaauth

// sessionEnvelope decodes the session/user portion shared by go-mivia's
// login, refresh, and verify response bodies (LoginResponseBody,
// RefreshResponseBody, VerifyResponseBody in openapi_types.gen.go). Those
// are three distinct generated schemas -- an artifact of how go-mivia's
// OpenAPI spec models each operation's response, not a meaningful
// difference for this client -- so decoding into this narrower,
// hand-written envelope instead of three near-duplicate typed decodes keeps
// doSessionRequest a single function. Session/User are pointers because
// LoginResponseBody and VerifyResponseBody both model them as optional
// (VerifyResponseBody's are absent under ErrVerifiedNoSession); JSON
// decoding into pointer fields here works the same whether the source
// schema declared them required or optional.
type sessionEnvelope struct {
	Session *SessionInfo `json:"session,omitempty"`
	User    *UserInfo    `json:"user,omitempty"`
}
