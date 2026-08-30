// Package miviaauth is the CLI's client for the mivia API's session
// endpoints, and the owner of the local session file at ~/.mivia/auth.json.
//
// The server contract is the NestJS API's /v1/auth/* surface: login and
// refresh exchange credentials for a short-lived bearer plus a long-lived,
// one-time-use refresh token; revoke ends the session; me reports the
// authenticated identity. The refresh token ROTATES on every use and the
// server treats a reused one as theft, so this package serializes the
// read-decide-write span around ~/.mivia/auth.json (see service.go) rather
// than letting two mivia processes refresh the same token concurrently.
//
// The recorded contract lives in api/contracts/auth.v1.json and is enforced
// against this package's routes and wire structs by wire_contract_test.go.
package miviaauth
