# Recorded API contracts

`auth.v1.json` records the mivia API's `/v1/auth/*` surface: one entry per
route (path, method, success status, whether it is public or bearer-gated) and
one entry per wire struct (the exact JSON field names, their value kinds, and
which are nullable).

`internal/miviaauth/wire_contract_test.go` holds the Go client to this file. It
runs on every `go test ./internal/miviaauth/...` -- no network, no sibling
checkout, no skip.

## What this catches, and what it does not

It catches the Go code drifting from the recorded contract.

It does **not** catch the recorded contract drifting from the live server. The
API is a separate repository (`mivia-app-web`) with a separate release cadence,
no shared CI, and no checked-in OpenAPI document -- NestJS's `SwaggerModule`
builds the spec at runtime. Nothing inside this repository can prove this file
still matches production. The generated types this replaced had exactly the
same limit and said so.

That is why this file is edited **by hand**. The test must never offer to
regenerate it from the Go structs: the whole guarantee is that one side of the
comparison is an artifact a person had to consciously change.

## Resyncing after an API change

Run the API locally and read the spec it serves:

```bash
curl -s http://localhost:3001/docs/json | python3 -m json.tool
```

Or read the source of truth directly -- the DTO and controller files listed
under `source.paths` in `auth.v1.json`. Update this file, then update
`internal/miviaauth` until `go test ./internal/miviaauth/...` passes again, and
refresh `source.transcribedOn`.

Request field sets are load-bearing rather than cosmetic: the API's global
`ValidationPipe` runs with `forbidNonWhitelisted`, so sending a property the
DTO does not declare is a `400`, not a silently dropped key.
