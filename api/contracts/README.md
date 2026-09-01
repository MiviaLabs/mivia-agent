# Recorded API contracts

`auth.v1.json` records the mivia API's `/v1/auth/*` surface: one entry per
route (path, method, success status, whether it is public or bearer-gated) and
one entry per wire struct (the exact JSON field names, their value kinds, and
which are nullable).

`chat-sessions.v1.json` records the `/v1/chat-sessions/*` surface the same way,
and adds an `events` section that describes the `mivia.chat.v1.*` event
vocabulary itself.

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

## The `events` section of `chat-sessions.v1.json`

The two halves of `chat-sessions.v1.json` have OPPOSITE directions of truth,
and the distinction decides how each is edited.

- `structs` records the API's response DTOs. `mivia-app-web` owns those, so the
  file is transcribed from that repository and the paragraph above applies: it
  is hand-edited on purpose.
- `events` records the `mivia.chat.v1.*` event vocabulary. This repository's
  projector EMITS those events and the API stores each payload as opaque
  `jsonb`, so nothing upstream defines their shape.
  `internal/chatsync.wireEventSpecs` is the authoring site and this section is
  its published mirror. `internal/chatsync/wire_events_test.go` derives the
  shape from the Go structs by reflection and fails on any difference, so the
  two cannot drift.

`events` carries three parts:

- `envelope` -- the fields every payload embeds. `v`, `at` and `turn` are on
  every frame.
- `objects` -- shared sub-objects a field points at through its `ref` key. For
  a field of kind `object` the `ref` names the object; for a map-valued field
  it names the shape of the map's VALUES, which is how `trunc.fields` reads.
- `types` -- one entry per event type, keyed by the exact wire string, holding
  the payload fields that sit alongside the envelope. `optional` is `true`
  when Go writes the field with `omitempty`, so an absent key is normal rather
  than a protocol error.

There is no static list of which fields may be truncated. A client learns that
per event from `trunc.fields`: a key present there was cut, and `kept`/`total`
give the byte counts. The budget that cut it is not on the wire.

### How a web client consumes this

Vendor the file. Copy `api/contracts/chat-sessions.v1.json` into the web
application and read it as data, or generate types from it there. Neither
repository then depends on the other's build, which is what the split between
`mivia-agent` and `mivia-app-web` requires.

This matters most for SSE. The API names each SSE frame after the event type
string, so `EventSource.onmessage` never fires for these events -- a client
must call `addEventListener` once per entry in `knownTypes`. A vocabulary the
client only half knows is an event that is emitted and never received.

Do not confuse this wire with `docs/product/wire-schema.md`. That documents the
NDJSON sidecar stream `mivia chat --json` writes to a local consumer. It is a
different transport with a different vocabulary, and the two must not be
merged.
