# Vendored OpenAPI specs

`auth.v2.yaml` is a byte-for-byte copy of go-mivia's checked-in
`api/openapi/auth.v2.yaml`, copied from commit `fe16084d7f3e787d280c3510deb9786a75e02418`
(2026-08-13).

go-mivia and mivia-agent are separate repos with no shared CI, so there is
no automated check that this copy still matches go-mivia's current spec.
`internal/miviaauth/openapi_types_freshness_test.go` only proves the
generated Go types in this repo match this vendored file -- it says
nothing about whether this file is still current relative to go-mivia.

To resync after a go-mivia identity/auth API change:

```
cp ../go-mivia/api/openapi/auth.v2.yaml api/openapi/auth.v2.yaml
go generate ./internal/miviaauth/...
```

Then update the commit hash and date above to go-mivia's
`git log -1 --format='%H %ad' --date=short -- api/openapi/auth.v2.yaml`.
