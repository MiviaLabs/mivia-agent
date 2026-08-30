# Contract fixtures

Response bodies used by `internal/miviaauth`'s tests. They pin the SHAPE of
each `/v1/auth/*` response, transcribed from the DTOs listed in
`api/contracts/auth.v1.json`.

Three deliberate departures from the DTOs' own `@ApiProperty` examples, so a
fixture is never a body the server could not send:

- `expiresAt` is a fixed timestamp here because these fixtures test decoding.
  Tests that reason about expiry (refresh windows, whoami's countdown) build
  their own value from the test clock instead — the DTO's example,
  `2026-08-30T11:00:00.000Z`, is a fixed past instant and would make those
  tests fail by wall-clock.
- `token` and `refreshToken` are full values. The DTO examples are truncated
  with a literal `...`; the real refresh token is exactly 64 hex characters
  (`randomBytes(32).toString("hex")`).
- `me_response_null_display_name.json` is synthesized. The DTO documents
  `displayName` as nullable but only ever exemplifies `"Jane Doe"`, and the
  null case is the one the Go pointer field exists for.
