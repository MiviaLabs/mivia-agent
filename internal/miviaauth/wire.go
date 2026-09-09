package miviaauth

import (
	"encoding/json"
	"strings"
	"time"
)

// The request and response bodies of the mivia API's /v1/auth/* endpoints.
//
// These are hand-written rather than generated: the API builds its OpenAPI
// document at runtime through NestJS's SwaggerModule and checks no spec into
// its repository, so there is no artifact to generate from offline. The
// recorded contract is api/contracts/auth.v1.json, and wire_contract_test.go
// holds these structs to it.
//
// Request structs must carry EXACTLY the fields their DTO declares. The API
// runs a global ValidationPipe with forbidNonWhitelisted set, so an extra
// property is a 400, not a silently ignored key.

// loginRequest is the body of POST /v1/auth/login (LoginDto).
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// refreshRequest is the body of POST /v1/auth/refresh (RefreshTokenDto).
type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// revokeRequest is the body of POST /v1/auth/revoke (RevokeTokenDto).
//
// RevokeTokenDto declares two optional fields, refreshToken and sessionId;
// this models only the first, because the CLI never revokes another session
// by id. There is deliberately no omitempty: the server reads an empty
// refreshToken as absent and falls back to the session named by the caller's
// own jti claim, which is the behaviour logout wants when it has no stored
// refresh token.
type revokeRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// authUser is the user summary embedded in a login or refresh response
// (AuthUserDto).
type authUser struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	OrganizationID string `json:"organizationId"`
	Role           string `json:"role"`
}

// sessionResponse is the body shared by login and refresh
// (LoginResponseDto and RefreshResponseDto, which are structurally
// identical). On refresh, RefreshToken is a NEW value: the one that was sent
// is dead server-side the moment this response is produced.
type sessionResponse struct {
	OK           bool      `json:"ok"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refreshToken"`
	User         authUser  `json:"user"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// okResponse is the body of a successful revoke (RevokeResponseDto). The
// flag is checked rather than ignored so an intercepting proxy's 200 with
// some unrelated body is not read as a successful revocation.
type okResponse struct {
	OK bool `json:"ok"`
}

// meResponse is the body of GET /v1/auth/me (MeResponseDto). DisplayName is
// a pointer because the column is nullable and the DTO declares
// `string | null`; a null decodes to nil rather than to the empty string, so
// "absent" and "set to empty" stay distinguishable at the wire boundary.
type meResponse struct {
	ID             string  `json:"id"`
	Email          string  `json:"email"`
	OrganizationID string  `json:"organizationId"`
	Role           string  `json:"role"`
	DisplayName    *string `json:"displayName"`
}

// errorEnvelope is the body the API's global HttpExceptionFilter returns for
// every non-2xx response. StatusCode is what distinguishes a real API
// rejection from a 401 injected by a captive portal or a corporate proxy --
// see StatusError.FromAPI, which is the gate on destroying a stored session.
// Message is a plain string for most errors and an array for the
// ValidationPipe's field-level rejections, so it is decoded loosely.
type errorEnvelope struct {
	StatusCode int             `json:"statusCode"`
	Error      string          `json:"error"`
	Message    stringOrStrings `json:"message"`
}

// stringOrStrings decodes the envelope's message field, which the API types
// as `string | string[]`: a single sentence for a thrown HttpException, and
// one entry per failed constraint for a ValidationPipe rejection. Anything
// else (a number, an object, null) decodes to the empty value rather than
// failing the whole envelope -- the envelope is read to CLASSIFY an error
// that already happened, so a surprising message shape must not turn a
// usable statusCode into a decode failure.
type stringOrStrings string

func (s *stringOrStrings) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = stringOrStrings(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*s = stringOrStrings(strings.Join(many, "; "))
		return nil
	}
	*s = ""
	return nil
}
