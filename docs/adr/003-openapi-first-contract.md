# 003 — OpenAPI-first contract shared by generation

**Status:** Accepted · 2026-10-04

## Context

With Go on one side and TypeScript on the other (ADR-001, ADR-002) the two
repositories can no longer share types by import. A field renamed in the API
must fail the site's type-check, not a visitor's booking.

## Options considered

- **Hand-written types on both sides.** Zero tooling. Lost because drift is
  silent until production.
- **Go structs as the source, OpenAPI generated from them.** Keeps Go
  idiomatic. Lost because the generated spec is only as good as struct tags,
  and the TypeScript side still lags one publish behind.
- **`openapi.yaml` as the source, code generated on both sides.** Spec is
  reviewed in the pull request like code; both sides regenerate from the same
  commit. Chosen.

## Decision

- `openapi.yaml` at the root of `vetmimi-api` is the contract. OpenAPI 3.1,
  one tag per domain, every response schema named, every error through one
  `Problem` schema (RFC 9457: `type`, `title`, `status`, `detail`, `errors[]`).
- Go: `make generate` runs `oapi-codegen` in **strict server** mode into
  `internal/httpapi/gen/` (committed) and `sqlc generate` into `internal/db/`
  (committed). CI runs `make generate-check`, which regenerates and fails on
  any diff.
- Requests are validated against the spec by `nethttp-middleware` before a
  handler runs, so handlers never re-validate shape; they validate business
  rules.
- TypeScript: `vetmimi-next` pins an API ref in `package.json`
  (`"vetmimiApi": {"ref": "v0.3.0"}`) and `pnpm api:types` fetches
  `openapi.yaml` at that ref and runs `openapi-typescript` into
  `lib/api/schema.ts` (committed); `openapi-fetch` gives a typed client in
  `lib/api/client.ts`, used only server-side (ADR-002). CI in `vetmimi-next`
  runs the same generation and fails on diff.
- Breaking changes bump the API minor version while pre-1.0, and the
  `vetmimi-next` pull request that adopts them bumps the pinned ref in the
  same change.

## Consequences

- Every API change starts in `openapi.yaml`; reviewers see the contract diff
  first.
- Two generated directories are committed; `generate-check` keeps them honest.
- Cross-repository features need two pull requests in order: API first
  (tagged), then site. The orchestrator sequences them.
