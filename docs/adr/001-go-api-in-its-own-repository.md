# 001 — Go API in its own repository

**Status:** Accepted · 2026-10-04, amended 2026-10-28 (Gin replaces chi and
oapi-codegen, ADR-011)

## Context

The public site is a Next.js 16 app on Vercel. The remaining scope — appointment
booking with availability rules and reminders, in-browser video sessions,
content management with approval and scheduled publishing, email — needs a
database, background workers and a long-lived WebSocket process. Vercel's
serverless functions cannot hold a WebSocket or run a worker, so a separate
always-on service is required whatever the language.

Constraints that shaped the choice:

- The product serves one practitioner and tens of bookings a month.
- Production will run on a single cheap host (ADR-005), and development on an
  8 GB laptop that also runs the Next.js dev server and several agents.
- The owner already has Java/Spring Boot proven in AU-Van and TypeScript/NestJS
  proven at EffortX; Go is on his résumé with no project behind it.
- The parent `AGENTS.md` had pencilled in a Java backend.

## Options considered

- **NestJS in the Next repository.** Shares TypeScript types with the site for
  free. Lost on footprint (~130 MB idle, 2 GB+ to type-check next to the
  Next.js build) and because it would repeat a stack the owner has already
  shown.
- **Spring Boot.** Familiar from AU-Van. Lost on footprint (~500 MB idle on a
  1 GB host) and because it duplicates that project on the résumé.
- **Go.** ~25 MB idle, seconds to compile and test, a standard library that
  covers HTTP, JSON, time zones and crypto, mature packages for the rest.
  Costs the shared TypeScript types, which ADR-003 recovers through
  generation.

## Decision

Build the API in Go, in its own public repository `VetMiMi/vetmimi-api`,
module path `github.com/VetMiMi/vetmimi-api`, current stable Go.

Approved dependencies, each with the job it does:

| Package | Job |
|---|---|
| `github.com/gin-gonic/gin` | router, middleware, request binding and validation (with `github.com/go-playground/validator/v10`) |
| `github.com/jackc/pgx/v5` | PostgreSQL driver and pool |
| `github.com/sqlc-dev/sqlc` (tool) | typed Go from SQL in `internal/db/queries` |
| `github.com/pressly/goose/v3` | SQL migrations, run at startup and by `make migrate` |
| `github.com/hibiken/asynq` | Redis-backed scheduled jobs: reminders, hold expiry, email delivery |
| `github.com/redis/go-redis/v9` | Redis client for asynq and rate limiting |
| `github.com/coder/websocket` | WebSocket signaling for video sessions |
| `github.com/pquerna/otp` | TOTP for the administrator sign-in |
| `golang.org/x/crypto/argon2` | password hashing |
| `github.com/resend/resend-go/v2` | transactional email |
| `github.com/aws/aws-sdk-go-v2/service/s3` | S3-compatible object storage for media |
| `github.com/disintegration/imaging` | web-ready derivatives of uploaded images |
| `github.com/stretchr/testify` | assertions |
| `log/slog`, `net/http`, `time`, `crypto/rand` (stdlib) | logging, server, time zones, tokens |

Anything else needs an ADR or a sentence in the pull request explaining what
code it removes.

The parent `AGENTS.md` is updated: `vetmimi-api/` replaces `backend-java/`.

## Consequences

- The site keeps deploying to Vercel untouched; the API deploys separately
  (ADR-005). Two repositories, one project board.
- TypeScript types for the site are written by hand in `vetmimi-next`
  (ADR-011) rather than imported; a change to a JSON shape updates both
  repositories together.
- Local development needs Go, PostgreSQL 17 and Redis from Homebrew; no Docker
  on the laptop.
- The owner's résumé entry for VetMiMi changes from NestJS to Go, PostgreSQL
  (pgx/sqlc), Redis (asynq) and WebSockets.
