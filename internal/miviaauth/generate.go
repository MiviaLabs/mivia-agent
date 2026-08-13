package miviaauth

// The request/response wire types in openapi_types.gen.go are generated from
// api/openapi/auth.v2.yaml, a vendored copy of go-mivia's checked-in
// identity/auth OpenAPI spec (go-mivia's own source of truth lives at
// internal/v2/identity/httpapi/, generated via
// internal/v2/identity/httpapi/cmd/genopenapi in that repo).
//
// go-mivia and mivia-agent are separate repos and separate release
// cadences, so there is no automated cross-repo freshness check here (see
// openapi_types_freshness_test.go for the in-repo half of that: it only
// proves openapi_types.gen.go matches api/openapi/auth.v2.yaml, not that
// the vendored spec matches go-mivia's current one). See
// api/openapi/README.md for the vendored file's source commit and the
// resync command.
//
//go:generate go tool oapi-codegen -generate types -package miviaauth -o openapi_types.gen.go ../../api/openapi/auth.v2.yaml
