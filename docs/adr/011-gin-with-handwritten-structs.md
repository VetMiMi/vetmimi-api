# 011 — Gin with handwritten structs, routes listed per feature

**Status:** Accepted · 2026-10-28. Supersedes ADR-003.

## Context

ADR-003 made `openapi.yaml` the contract and generated code from it on both
sides. For one practitioner's API that cost about 26,000 lines: a 6,000-line
spec and a 20,700-line generated strict server in `internal/httpapi/gen`.
Around it sat a middleware layer that looked up each request's operation in
an index built from the spec, and read authentication, roles, rate limits,
body caps and timeouts from tables keyed by operation id. Requests were
validated against the spec by kin-openapi, with workarounds for its OpenAPI
3.1 support.

To follow one route, a reader had to open the spec, the generated interface,
the middleware index, three lookup tables and the handler. The owner wants a
new developer to find their way in an afternoon and to be able to explain any
file in two minutes. The generated layer did not meet that bar.

## Options considered

- **Keep ADR-003.** The site's types are generated, so drift fails a
  type-check. Lost because the generated code and the spec-indexed layer are
  most of the HTTP code, and the hardest part to read.
- **Go structs as the source, OpenAPI generated from them.** Keeps generated
  TypeScript types. Lost because it keeps a generator and a spec in the loop,
  and the spec is only as good as the struct tags.
- **Standard library `net/http` with handwritten structs.** No dependency.
  Lost because binding, field validation and route groups would be written by
  hand.
- **Gin with handwritten request and response structs, routes listed per
  feature.** Chosen.

## Decision

- Gin is the router. `internal/web` holds what every route shares: the
  router, the route groups (`Public` with the service key; `Admin` signed in;
  `Slow` for uploads and the AI assistant; `Root` for the video WebSocket and
  health), `ServiceKey`, `SignedIn` and `Role`, rate-limit rules,
  `BindJSON`, `BindQuery`, `BindURI`, `PathUUID`, and `application/problem+json`
  errors through `web.Error`.
- Each feature package has `handler.go`, with a `Handler` struct and
  `Routes(r web.Routes)` listing every route with its group, role and rate
  limit, and `http_*.go` files with the handlers. Request and response structs
  are written by hand next to them; request structs carry their rules as
  `binding` tags.
- Sign-in, sign-out, `/auth/me` and health live in `cmd/api`, because
  `internal/web` imports `internal/auth`. `cmd/api/api.go` wires every feature.
- Handler tests go through `internal/webtest`: the real router and middleware.
- `openapi.yaml`, `oapi-codegen.yaml`, `internal/httpapi`, the contract-version
  check and the contract tag workflow are removed. `make generate` runs sqlc
  only. The JSON on the wire does not change.
- `vetmimi-next` calls the API through a small handwritten fetch client with
  handwritten TypeScript types. A change to a route or a JSON shape updates
  those types in the same change set.
- `STYLE.md` describes the pattern.

## Consequences

- One route can be followed in two files: the feature's `handler.go` and the
  handler in its `http_*.go`. Who may call it and its limit are on the same
  line as its path.
- The site's types are handwritten and can drift from the API without a
  build failure. We keep JSON shapes stable, change both sides together, and
  check the main flows end to end (booking, admin, publishing) after any
  change to a shape.
- No machine-readable description of the API is published. If an outside
  client ever needs one, it is a new decision.
- An admin route without `web.Role` is open to any signed-in user. Review
  checks that every admin route names its role.
