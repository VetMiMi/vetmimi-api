<div align="center">

# VetMiMi — API

**The Go API behind VetMiMi, an art therapy practice in Sydney. It handles appointment booking, WebRTC video sessions, a publishing portal for the website and social channels, media, and email.**

[Website](https://vetmimi-arts-therapy.vercel.app) · [Web app repo](https://github.com/VetMiMi/vetmimi-next) · [Architecture](docs/architecture.md) · [Project board](https://github.com/orgs/VetMiMi/projects/1)

[![CI](https://github.com/VetMiMi/vetmimi-api/actions/workflows/ci.yml/badge.svg)](https://github.com/VetMiMi/vetmimi-api/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?style=flat-square&logo=go&logoColor=white)
![PostgreSQL 17](https://img.shields.io/badge/PostgreSQL-17-4169E1?style=flat-square&logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-asynq-DC382D?style=flat-square&logo=redis&logoColor=white)
![OpenAPI 3.1](https://img.shields.io/badge/OpenAPI-3.1-6BA539?style=flat-square&logo=openapiinitiative&logoColor=white)
![WebRTC](https://img.shields.io/badge/WebRTC-signaling-333333?style=flat-square&logo=webrtc&logoColor=white)

</div>

---

## About

VetMiMi is Daw Mi's art therapy practice in Sydney. This repository is its backend: one Go binary that owns the database, admin sign-in, booking rules, video signaling, content and email. The Next.js app in [vetmimi-next](https://github.com/VetMiMi/vetmimi-next) is the only thing that calls it over HTTP. The one exception is the video WebSocket, which browsers open directly with a short-lived ticket.

It is designed for its real scale: one practitioner, tens of appointments a month, and one or two admins. It also aims to be correct under concurrency, and it never logs a client's personal data.

## Features

**Booking**
- **Services**, each bookable either directly or by enquiry, with durations and buffer times before and after a session.
- **Availability** from weekly hours, date overrides and blocks. Available slots are calculated in Sydney time and handle daylight saving correctly: the repeated hour in April and the missing hour in October are both tested.
- **Public booking requests** are safe to retry, because each one carries an idempotency key. If the slot was taken in the meantime, the API offers up to 5 alternative times within the next 14 days.
- **A pending request holds its slot** for 48 hours by default, until Daw Mi confirms or declines it.
- Admins can confirm, decline, reschedule, cancel, complete, mark a no-show, add notes and create appointments manually. Every change is checked against a status table and protected against two admins editing at once.
- **Clients manage their own appointment from a private link:** view it, cancel it, or ask to reschedule.
- **Contact enquiries**, plus a daily purge of old records (after 24 months by default).

**Video sessions**
- A private room is created for each confirmed online appointment. It opens 15 minutes before the start and closes 60 minutes after the end.
- Joining takes a 5-minute room ticket. The API relays offers, answers and ICE candidates over a WebSocket, and issues time-limited TURN credentials for coturn. Nothing is recorded.

**Publishing portal**
- **Posts** have versions for the website, Facebook, Instagram and LinkedIn. Each post goes through an editorial workflow: idea, draft, review, approved, scheduled, published.
- **Approval checks each platform's rules.** For example, Instagram needs an image and allows at most 2,200 characters and 30 hashtags, and a True Story needs the client's consent.
- **Publishing:**
  - to Meta (a Facebook Page and its linked Instagram account) and to LinkedIn, through OAuth connections
  - the website version becomes a public article, and the site refreshes the affected pages
  - if a channel isn't connected, Daw Mi gets the text to post herself
- **AI suggestions** draft each channel's version and translations through the Anthropic API. This is optional and turns off cleanly without a key.

**Media and email**
- **Image uploads** up to 20 MiB. EXIF data is stripped and the image is rotated upright, then resized to 1600, 800 and 400 px JPEGs in an S3-compatible bucket.
- **Transactional email** through Resend: 12 kinds of message, each in English and Burmese, with appointment reminders.

**Admin auth**
- **Sign-in needs** email, password (argon2id) and a **TOTP** code; accounts created with `--no-totp` sign in with the password alone until two-step setup moves into the admin website.
- **Sessions** expire after 12 hours idle or 7 days at most.
- **Lockouts and rate limits** slow down repeated failed sign-ins.
- **Roles:** site admin, booking admin and content editor.

## How it's built

- **The database prevents double-booking.** Each appointment stores the time it blocks, buffers included, and a PostgreSQL exclusion constraint rejects any overlap:

  ```sql
  EXCLUDE USING gist (practitioner_id WITH =, busy_range WITH &&)
    WHERE (status IN ('pending', 'confirmed'))
  ```

  A rejected overlap becomes a `409 slot_unavailable` response with alternative times. Changes to availability and new bookings also take turns through an advisory lock. A test races two writers for the same slot and checks that only one wins.
- **A pending request *is* the hold.** Its expiry time is stored on the row, and a CHECK constraint ties it to the pending status. The worker expires it on time, and a 5-minute sweep catches anything missed. A reschedule is a single `UPDATE`, so the new time is secured before the old one is released.
- **Emails are durable records** ([ADR-006](docs/adr/006-communications-as-durable-records.md)). Email rows are written in the same transaction as the change that caused them, and only the row id is queued. A sweep rebuilds anything Redis lost, the row id is Resend's idempotency key, and reminders are re-checked at the moment they are sent.
- **Links are safe even if the database leaks.** Manage and join links are derived with HMAC-SHA256 from a stored seed. The database keeps only the seed and a hash of each link, so a database dump contains no working links.
- **The OpenAPI contract drives security.** Each of the contract's 89 operations must declare how callers authenticate, or the server refuses to start. Access rules follow each operation's declaration, not its URL path. A test checks that every admin operation has its roles set; an operation without them is refused to everyone. Requests are validated against the spec, and errors are returned as `application/problem+json` with stable codes.
- **Signaling** ([ADR-007](docs/adr/007-one-to-one-webrtc-with-go-signaling.md)):
  - one connection per role per room
  - a reconnect replaces the old socket
  - frames are capped at 16 KiB, with pings and a check on the page's origin
  - each room allows 20 connection attempts a minute
  - the server re-reads room state every 30 s, so the worker can end sessions
- **Privacy-preserving rate limits.** Requests are counted per minute in Redis under hashed keys, so no IP address or email address is stored. Sign-in fails closed when Redis is down; other requests fail open.
- **The contract is versioned across repos** ([ADR-003](docs/adr/003-openapi-first-contract.md)). CI rejects a change to `openapi.yaml` that doesn't raise its version, and merging tags the new version. The web app pins that tag and regenerates its client types from it.

## Tech stack

| Layer | Technology |
|---|---|
| Language | Go 1.27 (one binary: `api`, `worker` and `create-user` modes) |
| HTTP | chi, oapi-codegen (strict server from `openapi.yaml`), kin-openapi request validation, coder/websocket |
| Data | PostgreSQL 17 (pgx, sqlc, goose migrations embedded in the binary), `btree_gist` exclusion constraints |
| Jobs | Redis + asynq (email, reminders, hold expiry, publishing, sweeps, retention) |
| Integrations | Resend (email), S3-compatible storage, coturn (TURN), Meta Graph API, LinkedIn API, Anthropic Messages API |
| Security | argon2id, TOTP (pquerna/otp), AES-256-GCM for stored secrets, HMAC-derived tokens |
| Quality | `go test -race`, real-PostgreSQL integration tests, staticcheck, golden-file email templates |

## Architecture

```mermaid
flowchart LR
  next["vetmimi-next<br/>(server side)"] -->|"X-Service-Key or<br/>Bearer session"| api["api mode<br/>chi + strict OpenAPI server"]
  browser["Browser"] -.->|"video WebSocket<br/>5-min ticket"| api
  api --> pg[("PostgreSQL 17")]
  api -->|"enqueue after commit"| redis[("Redis")]
  redis --> worker["worker mode<br/>asynq"]
  worker --> pg
  worker --> resend["Resend"]
  worker --> social["Meta / LinkedIn"]
  worker -->|"revalidate tags"| next
  api --> s3[("S3 bucket")]
```

- **Domain packages** in `internal/`: `booking`, `video`, `content`, `media`, `comms`, `auth`, `meta`, `linkedin`, `assistant`. They never import `net/http`, and they return typed errors that `internal/httpapi` maps to Problem responses.
- **Shared code:** `internal/platform` holds config, the database pool, Redis, the rate limiter, the queue, logging and idempotency. `internal/db` is generated by sqlc.
- **Database:** 19 migrations. Statuses are `text` with a `CHECK`, admin-edited rows carry a `version` for optimistic locking, and translatable text is a `jsonb` with `en` and `my` keys.
- **Request pipeline**, in order:
  1. request id, real IP, logging (route pattern only) and panic recovery
  2. 30 s timeout and a body size cap
  3. auth chosen from the operation's declaration
  4. roles and rate limits
  5. OpenAPI validation, then the handler

See [`docs/architecture.md`](docs/architecture.md) for the full component table and walkthroughs of booking, confirmation, reminders, video join and publishing. See [`docs/data-model.md`](docs/data-model.md) for every table.

## Getting started

**Prerequisites:**
- Go 1.27
- PostgreSQL 17 and Redis (Homebrew is fine; Docker isn't needed locally)
- *optional:* an S3-compatible endpoint such as MinIO, for media uploads

```sh
brew services start postgresql@17 && brew services start redis
createdb vetmimi && createdb vetmimi_test
make tools                         # pinned sqlc, oapi-codegen, goose, staticcheck
cp .env.example .env               # fill in the values
set -a; . ./.env; set +a           # nothing loads .env automatically: export it into your shell
make dev                           # API on :8080, migrates on start → curl localhost:8080/healthz
make worker                        # in a second terminal: background jobs
```

**Create the first admin.** Run this in a terminal (it needs a TTY):

```sh
go run ./cmd/api --mode create-user --email you@example.com --name "Your Name" \
  --roles site_admin --practitioner
```

It asks for a password twice, prints an `otpauth://` URI for your authenticator app, and saves nothing until you enter a valid code. `--practitioner` marks the person appointments are booked with, and booking needs exactly one. Running it again for the same email resets the password and TOTP secret and signs that user out everywhere. `--no-totp` skips the authenticator step and saves (or resets to) a password-only account.

**Environment variables:** [`.env.example`](.env.example) lists each one with its purpose.

| Group | Variables |
|---|---|
| Always required | `DATABASE_URL`, `REDIS_URL`, `SERVICE_KEY`, `SIGNING_SECRET` (32+ bytes), `TOTP_ENCRYPTION_KEY` (base64 of 32 bytes), `PUBLIC_API_URL`, `SITE_URL` |
| Required in production | `SITE_REVALIDATE_SECRET`, `RESEND_API_KEY`, `EMAIL_FROM`, `MEDIA_S3_*`, `MEDIA_PUBLIC_URL`, `TURN_HOST`, `TURN_SECRET` |
| Optional | `META_APP_ID` + `META_APP_SECRET`, `META_CONFIG_ID`, `LINKEDIN_CLIENT_ID` + `LINKEDIN_CLIENT_SECRET`, `ANTHROPIC_API_KEY`, `PORT`, `LOG_LEVEL` |
| Tests | `DATABASE_URL_TEST`, `REDIS_URL_TEST` |

On startup the config check reports every problem at once and never prints a value. In development, email without a Resend key is only logged, without contents, and media uploads are refused when no bucket is configured.

**Calling it:** public routes need the `X-Service-Key: $SERVICE_KEY` header (for example `GET /public/booking/services`). Admin routes need a `Bearer` token from `POST /auth/sessions`.

Setting up the social connections: [`docs/meta-setup.md`](docs/meta-setup.md), [`docs/linkedin-setup.md`](docs/linkedin-setup.md).

## Testing

```sh
make gate                          # lint + test + build + generated-code drift check
make test-pkg PKG=./internal/booking
go test ./internal/comms -update   # regenerate email golden files
```

- **About 450 tests in 97 files.** Integration tests run against **real PostgreSQL**, with no mocks: each test binary gets its own migrated database, which is dropped afterwards. If the test databases aren't configured, the run fails rather than skipping.
- **Every external API is tested against a fake HTTP server:** Resend, Meta, LinkedIn and Anthropic.
- **Each behaviour gets a success test and a failure test**, and anything touching scheduling gets a concurrency test.
- **CI** (`.github/workflows/ci.yml`) runs on PRs and on pushes to `main`:
  - **API checks** on Postgres 17 and Redis 7: `make tools`, `make lint`, `make generate-check`, `go build ./...`, `go test -race ./...`
  - **Deployment checks:** shellcheck, the `deploy.sh` and compose tests, `caddy validate`, an image build, and `terraform validate` for each root
  - **Contract version:** an `openapi.yaml` change must raise its version
  - **Commit checks:** every commit message follows the Conventional Commits format
  - `contract-tag.yml` tags each new contract version on `main`

## Deployment

Written and checked, not yet applied: [ADR-010](docs/adr/010-live-host-on-ec2-with-the-website.md) and the runbook in [`deploy/README.md`](deploy/README.md).

- **Live host:** one AWS EC2 `t4g.small` in Sydney, described in Terraform (`deploy/terraform/live`), running Docker Compose with:
  - the API and the worker, from the same image
  - the website (`vetmimi-next`)
  - PostgreSQL 17 and Redis 7
  - Caddy for TLS
  - coturn for TURN
- **Releases:** after CI passes on `main`, `release.yml` builds an arm64 image, pushes it to ECR through GitHub's OIDC role and deploys over SSH. A release that fails its health check within a minute is rolled back automatically.
- **Backups:** a nightly `pg_dump` to a private S3 bucket, kept 30 days.
- **Alarms:** CloudWatch emails the owner on failed status checks (recovering or rebooting the instance), high CPU, memory or disk.
- **Later target:** ECS Fargate behind an ALB, with RDS, written in Terraform. CI validates it but never applies it.

## Project status

The foundation, booking and contact, video, and the publishing portal milestones are complete. Launch work is in progress:

- deployment and backups
- a runbook and handover guide
- Daw Mi's review of the English and Burmese emails

Five operations are already in the contract but not served yet: the booking dashboard, manual meeting links, and the communications list, resend and mark-as-sent. See the [project board](https://github.com/orgs/VetMiMi/projects/1) and [open issues](https://github.com/VetMiMi/vetmimi-api/issues).

## Documentation

- [`docs/architecture.md`](docs/architecture.md): components, request flows, error codes, deployment summary
- [`docs/data-model.md`](docs/data-model.md): tables and constraints
- [`docs/adr/`](docs/adr): decision records 001–009
- [`AGENTS.md`](AGENTS.md): working agreement, layout and contribution rules
