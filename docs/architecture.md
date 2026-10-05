# Architecture

How the VetMiMi API is put together. Decisions and their reasons are in
`docs/adr/`; tables are in `docs/data-model.md`; the HTTP contract is
`openapi.yaml`. Where this document and an ADR disagree, the ADR wins.

## Purpose and scale

The API owns everything the public Next.js site cannot: the database,
administrator sign-in, appointment booking, online video sessions, content
management, media and email. It serves one practitioner (Daw Mi), one or two
administrators, tens of appointments a month and a few dozen content records.

That scale sets the design: one HTTP process, one job process, PostgreSQL as
the source of truth, Redis for queues and rate limits only. Correctness comes
from database constraints and small transactions, not from clever Go.

## Components

| Component | Role |
|---|---|
| **api process** | `api --mode api` (the default). chi router, all HTTP routes, the video signaling WebSocket. Stateless apart from the in-memory video room hub. |
| **worker process** | The same binary with `--mode worker`. Runs the asynq server: email delivery, hold expiry, scheduled publishing, site revalidation, media derivatives, sweeps and clean-up. Never serves HTTP. |
| **PostgreSQL 17** | Every durable fact: settings, users, sessions, services, availability, appointments, communications, content, media metadata. Enforces "no overlapping appointments" with an exclusion constraint (ADR-004). |
| **Redis 7 + asynq** | Task queues (`critical`, `default`, `low`) and rate-limit counters. Losing Redis loses only queued tasks; sweepers rebuild them from PostgreSQL (ADR-006). |
| **Resend** | Transactional email. Called only by the worker. |
| **S3-compatible bucket** | Media originals (private prefix `originals/`) and web derivatives (public prefix `public/`), served from `MEDIA_PUBLIC_URL`. |
| **coturn** | TURN relay on the live host for video sessions; the API mints time-limited credentials with the shared `TURN_SECRET` (ADR-007). |
| **Next.js site** (`vetmimi-next`) | The browser's only HTTP origin (ADR-002). Its server components and route handlers call this API server-side with `X-Service-Key` (public routes) or `Authorization: Bearer` (admin routes). The API has no CORS. |
| **WebSocket exception** | `GET /video/rooms/{roomId}/ws?ticket=…` is the one route a browser calls directly, with a five-minute room ticket that Next obtained server-side (ADR-007). |

Other entry points: `cmd/import-content` (one-off migration of the site's JSON
into content tables, ADR-008) and `api --mode create-user`, which creates an
administrator, or replaces an existing one's password and TOTP secret. It reads
the password from standard input, never a flag, prints the `otpauth://` URI and
saves nothing until a valid code is typed back.

## Package layout

One package per domain under `internal/`, matching `AGENTS.md`. Domain packages
take `context.Context` and a `*pgxpool.Pool` or `db.Querier`, return typed
errors from `internal/platform/apperr`, and never import `net/http`.

| Package | Owns |
|---|---|
| `platform` | Configuration from the environment, the pgx pool, the Redis client, the rate limiter, the asynq client, the slog logger, the clock (`platform/clock`, injectable in tests), id and token generation, `platform/apperr` (typed errors with a stable code), `platform/settings` (typed read and write of the `settings` table). |
| `httpapi` | The generated strict server (`httpapi/gen`, from `openapi.yaml`), handlers that parse → call a domain function → map the result, middleware (auth, rate limits, body caps, security headers, request logging), and the single `apperr` → `Problem` mapping. |
| `auth` | Users, password hashing (argon2id), TOTP enrolment and verification, sessions, roles, the sign-in throttle. |
| `booking` | Services, availability rules, overrides and blocks, slot generation, appointments, the status transition table (`status.go`), appointment events, management tokens, hold expiry, retention purge. |
| `video` | Video rooms, join tokens, room tickets, TURN credentials, the in-memory room hub and the WebSocket signaling loop, room closing. |
| `content` | Stories, service pages, portfolio items, pages and sections, publication and approval state, versions, relations, featured slots, slug redirects, Facebook post tracking, scheduled publishing, site revalidation. |
| `media` | Uploads, type sniffing, derivative generation, object storage, permission metadata, usage checks. |
| `comms` | Communication records, templates (`comms/templates/<kind>.<locale>.tmpl`), delivery through Resend, the sweeper, contact enquiries. |
| `db` | sqlc output (generated, committed) and `db/queries/*.sql`. |

Domain functions that cause background work return the tasks to enqueue; the
handler enqueues them **after** the transaction commits. Nothing enqueues from
inside a transaction.

## Request lifecycle

chi middleware runs in this order for every HTTP route:

1. **Request id** — reuse a valid incoming `X-Request-Id` or generate one; echo it in the response.
2. **Real IP** — trust `X-Forwarded-For` from Caddy only (the API port is not exposed elsewhere).
3. **Logger** — one slog line per request after it completes: request id, method, chi route pattern (never the raw path, so tokens in paths are never logged), status, duration, bytes.
4. **Recover** — a panic becomes `500 internal_error` and an error log with the stack.
5. **Timeout** — 30 seconds, and the request context carries it into every query. Media upload gets 120 seconds. The WebSocket route is mounted outside this group.
6. **Body cap** — `http.MaxBytesReader`: 64 KiB for JSON, 512 KiB for content saves (rich text), 21 MiB for media upload.
7. **Authentication** — `X-Service-Key` for `/public/*` and `POST /auth/sessions`; `Authorization: Bearer <session>` for `/admin/*` and the other `/auth/*` routes. Then the role check for the route group, then the rate limiter for the group.
8. **OpenAPI validation** — `nethttp-middleware` validates path, query, headers and body against the embedded spec. Shape errors become `400 invalid_request` with `errors[]`.
9. **Handler** — strict-server handler; calls one domain function; maps the result or the `apperr` to a response.

Steps 7 and 8 run per operation, through the generated
`ChiServerOptions.Middlewares`. Step 7 finds the request's operation in an
index built once from the embedded spec and keyed by method and path
template, which is exactly the chi route pattern, so a route is guarded by
the `security` its operation declares, never by its path. Every operation
declares exactly one of `serviceKey`, `sessionToken` or `security: []`; the
index refuses anything else at start-up. The generated wrapper wraps each entry around
the ones before it, so the last entry runs first: validation is
`Middlewares[0]` and runs last, just before the handler, and the
authentication, role and rate-limit middlewares are appended after it. The
validator checks the `email`, `date` and `date-time` formats by parsing the
value (`net/mail`, `time.Parse`), not by kin-openapi's loose patterns, and
`uuid` against the RFC 9562 layout. It hands kin-openapi the
spec as OpenAPI 3.0: for a 3.1 document kin-openapi's JSON Schema 2020-12
engine reduces each failure to a sentence quoting the value, losing the rule
and the JSON pointer, and `openapi.yaml` uses only keywords both engines read
alike. Every message in `errors[]` is built from the rule, never from an error
text, so a submitted value is never echoed. Parameters bind in the generated
wrapper before any of these middlewares run; those that fail to bind, and
bodies the strict server cannot decode, answer
`is missing or malformed` for the same reason; the log keeps only the error
types.

### Error model

Every 4xx and 5xx body is an RFC 9457 `Problem` (`application/problem+json`):
`type` (`urn:vetmimi:problem:<code>`), `title`, `status`, `detail`, `code` and
`errors[]` of `{field, message}`. The site maps `code` to localised wording and
never shows `detail` to visitors. Renaming a code is a breaking change.

| Code | Status | Meaning |
|---|---|---|
| `invalid_request` | 400 | Body, query, header or path does not match the spec. |
| `unauthenticated` | 401 | Missing or wrong service key, or missing, expired or revoked session. |
| `invalid_credentials` | 401 | Sign-in failed. Same response for unknown email, wrong password and wrong TOTP code. |
| `forbidden` | 403 | Signed in, but no role grants this route. No resource data is returned. |
| `not_found` | 404 | Unknown id or slug, or an invalid or expired management or session token (neutral by design). |
| `slot_unavailable` | 409 | The requested time is no longer free. Carries `alternatives[]`. |
| `stale_version` | 409 | The `version` sent does not match the record; someone else changed it. Refresh and retry. |
| `invalid_transition` | 409 | The status change is not allowed from the current status. |
| `slug_taken` | 409 | Another record of the same type already uses the slug. |
| `overlapping_period` | 409 | A weekly availability period overlaps another on the same weekday. |
| `in_use` | 409 | Delete refused: media used by content, or a service with appointments. Archive instead. |
| `payload_too_large` | 413 | Body or upload over its cap. |
| `unsupported_media_type` | 415 | Upload type not accepted (JPEG, PNG, WebP, PDF only). |
| `outside_booking_window` | 422 | Start time inside the minimum notice or beyond the maximum advance window. |
| `service_not_bookable` | 422 | Service paused, archived, or its booking action is not `book` or `request`. |
| `booking_paused` | 422 | `public_booking_enabled` is false. |
| `acknowledgement_required` | 422 | Privacy or booking policy acknowledgement missing. |
| `idempotency_key_reused` | 422 | Same `Idempotency-Key` sent with a different body. |
| `action_not_allowed` | 422 | Business rule refuses the action, e.g. cancelling a past appointment through a management link. |
| `approval_required` | 422 | Publish or schedule requested while `approval_status` is not `approved`. |
| `publish_requirements_unmet` | 422 | Hard publishing checks failed; `errors[]` lists each one. |
| `website_not_published` | 422 | A Facebook post that links to website content cannot be marked published before that content is. |
| `rate_limited` | 429 | Too many requests for this route group; `Retry-After` is set. |
| `internal_error` | 500 | Unexpected failure. Logged with the request id; nothing was half-saved. |
| `unavailable` | 503 | PostgreSQL or Redis unreachable. |

## Authentication and roles

**Service key.** Next sends `SERVICE_KEY` as `X-Service-Key` on public calls
and sign-in; the API compares SHA-256 digests in constant time, so neither
content nor length leaks, and answers a missing or wrong key with
`401 unauthenticated`. The visitor's address arrives in `X-Visitor-IP`,
trusted only alongside a valid service key; when it is missing or does not
parse, the real IP stays and `visitor_ip_missing` is logged with the route.

**Sessions.** `POST /auth/sessions` takes email, password and TOTP code and
returns an opaque token; only its SHA-256 is stored. Next keeps it in an
`httpOnly`, `Secure`, `SameSite=Lax` cookie and forwards it as a bearer token.
Sessions expire after 12 hours idle (ADR-002) or 7 days in all. Sign-out
deletes the row.

**Passwords and TOTP.** argon2id (64 MiB, 3 iterations, parallelism 2, PHC
string). TOTP (30 s, 6 digits, ±1 step) is required for every user and enrolled
by `api --mode create-user`, so the API has no enrolment routes. Secrets are
encrypted with `TOTP_ENCRYPTION_KEY` (AES-256-GCM); the last accepted step is
stored against replay. Ten failures for one email in 15 minutes lock it for 15.

**Roles** (ADR-002). One user may hold several. `site_admin` implies the other two.

| Route group | content_editor | booking_admin | site_admin |
|---|---|---|---|
| `/auth/me`, sign-out | yes | yes | yes |
| `GET /admin/settings` | `site` keys only | yes | yes |
| `PATCH /admin/settings` | no | `booking` keys | all keys |
| `/admin/services`, `/admin/availability/*` | no | yes | yes |
| `/admin/appointments/*`, `/admin/dashboard`, `/admin/communications` | no | yes | yes |
| `/admin/contact-enquiries` | no | yes | yes |
| Content list, get, create, patch, submit-for-review, versions, restore, relations | yes | no | yes |
| Content approve, request-changes, publish, schedule, unpublish, archive; featured slots | no | no | yes |
| `/admin/media` (upload, patch, archive) | yes | no | yes |
| `DELETE /admin/media/{id}` | no | no | yes |
| `/admin/facebook-posts` create, patch, status up to `permission_pending` | yes | no | yes |
| Facebook status `approved`, `scheduled`, `published`, `withdrawn` | no | no | yes |

Content editors never see booking data; booking administrators never edit
content. Daw Mi holds all three roles.

## Sequence walkthroughs

### 1. Public booking request submit

1. The browser generates a UUID idempotency key when the review step mounts,
   kept across double-clicks and refreshes.
2. Next calls `POST /public/appointments` with `X-Service-Key`, `X-Visitor-IP`
   and `Idempotency-Key`; middleware rate-limits and validates the body.
3. `booking.RequestAppointment` opens a transaction and inserts an
   `idempotency_keys` row (scope `public_appointment`, SHA-256 of the body). If
   the key exists, the insert waits for the other transaction, then returns the
   stored response, or `422 idempotency_key_reused` if the hash differs. A failed
   attempt rolls back its key, so a retry is evaluated afresh.
4. The service must be `active` with action `book` or `request`,
   `public_booking_enabled` true, and the start inside `min_notice_hours` and
   `max_advance_days`; otherwise `422`.
5. The transaction takes `pg_advisory_xact_lock` on the practitioner id (the same
   lock availability writes take), recomputes availability for that local date,
   and requires the requested slot to be in it. If not: `409 slot_unavailable`
   with up to five alternatives on the same or following days.
6. Go computes `busy_range` = [start − buffer before, end + buffer after), picks
   the status (`pending`, or `confirmed` only when `booking_mode = instant` and
   the service action is `book`), sets `hold_expires_at` = the earlier of now +
   `pending_hold_hours` and the start time, generates the reference code and the
   management-token seed and hash, and inserts the appointment. A violation of
   `appointments_no_overlap` (SQLSTATE 23P01) is mapped to `409 slot_unavailable`.
7. It inserts an `appointment_events` row (`created`) and two `communications`
   rows: `request_received` to the visitor and `practitioner_new_request` to
   `contact_email`, both `queued`.
8. It stores the response in the idempotency row and commits.
9. After commit, the handler enqueues `comms:deliver` per communication (task
   id `comms:<id>`) and `booking:expire-hold` at `hold_expires_at` (task id
   `hold:<appointment id>`). An enqueue failure is left to the sweepers.
10. Response `201`: reference, status, service, start, end, duration, format and
    `timezone`. Next shows "Appointment request received — Status: Pending".

### 2. Admin confirm

1. Daw Mi opens the pending request and presses Confirm. Next calls
   `POST /admin/appointments/{id}/confirm` with the bearer token and the
   `version` it displayed.
2. Session and `booking_admin` role are checked.
3. In one transaction: `SELECT … FOR UPDATE`; a different `version` gives
   `409 stale_version`; the transition table must allow `pending → confirmed`
   and the start must be in the future, otherwise `409 invalid_transition`.
4. Status becomes `confirmed`, `hold_expires_at` is cleared, `version`
   increments. The row stays inside the exclusion constraint, so nothing can
   take its time between the two states.
5. If the format is `online` and `meeting_link_mode = vetmimi_room`, a
   `video_rooms` row is inserted with a join-token seed and hash, `opens_at` =
   start − 15 minutes, `closes_at` = end + 60 minutes. In `manual_link` mode the
   request may carry `meetingLink`; without one the dashboard lists the
   appointment under "Attention required".
6. Events row `confirmed`; communications `booking_confirmed` (visitor) and
   `reminder` (visitor, `scheduled_for` = start − `reminder_hours`, only if that
   is still in the future).
7. Commit. Enqueue `comms:deliver` for the confirmation now and for the
   reminder at `scheduled_for`; delete the `hold:<id>` task (if it fires anyway it
   re-checks status and does nothing).
8. The worker renders the confirmation in the visitor's locale, re-derives the
   join link from the room's seed, sends through Resend (idempotency key = row
   id) and records `sent`, or retries 5 times and records `failed`. A failed
   email never reverts the confirmation; the detail page shows "Appointment
   confirmed. Visitor notification failed." with Resend and Mark as
   communicated.

### 3. Reminder firing

1. asynq runs `comms:deliver` for the reminder at `scheduled_for`. If Redis lost
   the task, `comms:sweep` (every 5 minutes) enqueues any `queued` row whose
   `scheduled_for` is more than a minute old.
2. The worker locks the row (`FOR UPDATE`) and skips unless it is `queued`.
3. It loads the appointment and re-checks: status `confirmed`, start in the
   future, and `start − reminder_hours` equal to the row's `scheduled_for` (so a
   reschedule's superseded reminder never fires). If any check fails the row
   becomes `cancelled` with the reason in `error`.
4. It renders the reminder with the join link (VetMiMi room) or `meeting_link`
   (manual mode), sends, and records the result as in walkthrough 2.

### 4. Video join

1. The visitor opens `SITE_URL/session/<token>` (`/my/session/<token>` in
   Burmese). Next calls `GET /public/sessions/{token}`. An unknown token gives
   `404`; otherwise the state is `too_early`, `ready`, `expired` or `ended`, with
   `opensAt`, `closesAt`, the appointment times and `timezone`.
2. When `ready`, Next calls `POST /public/sessions/{token}/ticket`. The API
   checks the token hash, the window, that the appointment is `confirmed` and the
   room not `ended`, and returns a room ticket, the WebSocket URL and ICE servers.
3. Daw Mi starts from the appointment in `/admin`: Next calls
   `POST /admin/appointments/{id}/video-session` and gets the same shape with the
   participant role `practitioner`.
4. The ticket is an HMAC-signed payload (room id, role, 5-minute expiry). TURN
   credentials follow coturn's REST convention: username `<expiry>:<room>:<role>`,
   credential `base64(HMAC-SHA1(TURN_SECRET, username))`, valid to `closes_at`.
5. The browser opens `wss://<api host>/video/rooms/{roomId}/ws?ticket=…`. The
   handler (outside the timeout and OpenAPI middleware) requires `Origin` =
   `SITE_URL`, a valid ticket for this room and an open room, then upgrades.
6. The hub keeps one connection per role per room; a new one closes the old
   with code 4000 (`replaced`), which is how reconnects work.
7. The server sends `peer-state` to both sides on every join and leave. When
   both are present the room becomes `in_session` and `started_at` is set.
8. `join`, `offer`, `answer`, `ice` and `leave` messages (16 KiB cap) are relayed
   to the other participant and never stored or logged.
9. Daw Mi ends the session with `POST /admin/appointments/{id}/video-session/end`,
   or `video:close-room` ends it at `closes_at`. Both close the sockets and set
   the room `ended`. The appointment becomes `completed` only when Daw Mi marks it.

### 5. Publish a story

1. Daw Mi presses Publish. Next calls `POST /admin/stories/{id}/publish` with
   the `version`; the API checks the session and the `site_admin` role.
2. In one transaction: lock the row, check `version`, check the publication
   transition is allowed, and require `approval_status = approved`
   (`422 approval_required`).
3. Hard publishing checks: English title, slug, excerpt and body present; for
   category `true_stories`, storyteller approval, website permission and privacy
   review complete; every linked image has website permission `approved` or
   `not_required`, is not `do_not_use` or archived, and has English alt text. Failures give
   `422 publish_requirements_unmet` listing each check.
4. The public projection (public fields only, all locales, media ids) is
   written to `published_snapshot`; `publication_status` becomes `published`;
   `published_at` is set on first publication, `last_published_at` and
   `published_by` every time; `published_version` records the row version. A
   `content_versions` row (`publish`) is written. If the slug differs from the
   last published one, a `slug_redirects` row is added.
5. Commit. Enqueue `site:revalidate` with tags `stories`, `story:<slug>` (and
   the old slug's tag), and `featured:<area>` for every featured area holding
   the story.
6. The worker posts `{ "tags": [...] }` to `SITE_URL/api/revalidate` with
   `X-Revalidate-Secret`, retrying 5 times.
7. Scheduling is the same path split in two: `schedule` stores `publish_at` and
   sets `scheduled`; `content:publish-scheduled` runs steps 2–6 at `publish_at`
   as the system user, and if approval was reset by a later edit it moves the
   record back to `review` and lists it under "Attention required" instead.

## Time handling rules

- Every instant is `timestamptz` and every JSON timestamp is RFC 3339 in UTC
  (`Z`). Responses about appointments and availability also carry `timezone`.
- The practice timezone is `settings.timezone` (`Australia/Sydney`), applied
  with `time.LoadLocation`. Nothing adds or subtracts hours to convert.
- Weekly availability rules are the one wall-clock value: weekday plus local
  start and end time. Slot generation expands them per local date with
  `time.Date(…, loc)`. One-off openings, blocks and appointments are instants.
- Slots are generated by stepping `slot_step_minutes` through each local
  period, converting each candidate to UTC, dropping candidates whose local time
  does not exist (October gap) and duplicates from the repeated hour (April).
  Tests cover both Sydney transition Sundays.
- Public availability takes `from` and `to` as local dates in the practice
  timezone; the response lists slot instants in UTC.
- Durations and offsets (`reminder_hours`, `pending_hold_hours`,
  `min_notice_hours`) are absolute hours, so a 24-hour reminder is 24 real hours
  even across a daylight-saving change.
- Dashboard "today" uses the local day. Emails show local time with AEST or
  AEDT. All "now" comes from `platform/clock`, so tests can fix it.

## Background jobs

| Task | Trigger | Schedule | Idempotency |
|---|---|---|---|
| `comms:deliver` | After commit of any communication row; reminder rows at `scheduled_for` | Immediate or `ProcessAt` | Task id `comms:<id>`; worker locks the row and skips unless `queued`; Resend idempotency key = row id |
| `comms:sweep` | asynq periodic | Every 5 minutes | Enqueues `queued` rows due more than a minute ago with the same task ids |
| `booking:expire-hold` | Appointment created as `pending` | At `hold_expires_at` | Task id `hold:<id>`; acts only if still `pending` and the hold has passed; writes `expired`, event and `request_expired` email in one transaction |
| `booking:sweep-holds` | asynq periodic | Every 5 minutes | Runs the same expiry for any overdue `pending` row |
| `content:publish-scheduled` | `schedule` action | At `publish_at` | Task id `publish:<type>:<id>:<version>`; acts only if still `scheduled` with the same `publish_at` |
| `content:sweep-scheduled` | asynq periodic | Every 5 minutes | Publishes overdue `scheduled` rows through the same function |
| `site:revalidate` | After publish, unpublish, archive, featured change, service change | Immediate | Revalidating a tag twice is harmless |
| `media:derive` | After upload commit | Immediate, queue `low`, concurrency 1 | Skips unless `processing_status = processing`; overwrites the same object keys |
| `video:close-room` | Room created or moved | At `closes_at` | Task id `room:<id>:<closes_at>`; ends the room only if still open and the window has passed |
| `booking:purge-retention` | asynq periodic | Daily 03:00 practice time | Deletes appointments and contact enquiries past `retention_months`; deleting twice deletes nothing |
| `platform:cleanup` | asynq periodic | Hourly | Deletes expired sessions and idempotency keys older than 24 hours |

## Configuration

Read once at start-up by `platform`; a missing required variable stops the
process with a clear error. `.env.example` lists exactly these, with empty
values for secrets.

| Variable | Purpose |
|---|---|
| `PORT` | HTTP listen port (default 8080). |
| `ENV` | `development` or `production`. Production refuses to start without every secret. |
| `LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`. |
| `DATABASE_URL` | PostgreSQL connection string. |
| `DATABASE_URL_TEST` | Server for `go test`; each test binary creates and drops its own `vetmimi_test_<random>` database on it (`internal/platform/pgtest`). |
| `REDIS_URL` | Redis for asynq and rate limits. |
| `REDIS_URL_TEST` | Redis database used by tests (`/1`). Each test uses queue names and a rate-limit key prefix of its own and deletes only its own keys, never `FLUSHDB`, because test binaries share the database; a missing value fails the run. |
| `SERVICE_KEY` | Shared with the Next server; required on public routes and sign-in. |
| `SIGNING_SECRET` | 32+ random bytes; HMAC key for management and join tokens and room tickets. |
| `TOTP_ENCRYPTION_KEY` | 32 bytes, base64; encrypts TOTP secrets at rest. |
| `PUBLIC_API_URL` | The API's public origin, used to build the `wss://` URL in room tickets. |
| `SITE_URL` | The public site, for links in emails, the WebSocket `Origin` check and revalidation. |
| `SITE_REVALIDATE_SECRET` | Shared with the site's `POST /api/revalidate`. |
| `RESEND_API_KEY` | Resend API key. Empty in development: emails are logged as "would send" (kind and id only). |
| `EMAIL_FROM` | Daw Mi's approved sender, e.g. `VetMiMi <hello@…>`. Reply-to is `settings.contact_email`. |
| `MEDIA_S3_ENDPOINT` | S3-compatible endpoint. |
| `MEDIA_S3_REGION` | Region name the provider expects. |
| `MEDIA_S3_BUCKET` | Bucket for originals and derivatives. |
| `MEDIA_S3_ACCESS_KEY` | Bucket access key. |
| `MEDIA_S3_SECRET_KEY` | Bucket secret key. |
| `MEDIA_PUBLIC_URL` | Base URL the bucket's `public/` prefix is served from. |
| `TURN_HOST` | coturn `host:port`. |
| `TURN_SECRET` | coturn `static-auth-secret` for time-limited credentials. |
| `METRICS_ADDR` | Optional `127.0.0.1:9090`; serves counters at `/debug/vars`. Empty disables. |

## Observability

- **Logs**: slog JSON to stdout with `request_id` (HTTP) or `task` and
  `task_id` (worker). Domain logs name ids, never people. The request id is
  echoed in `X-Request-Id`, so an administrator's report can be traced.
- **`/healthz`** (liveness): returns `200 {"status":"ok"}` if the process can
  serve; it touches no dependency. The deploy rollback (ADR-005) polls it.
- **`/readyz`** (readiness): pings PostgreSQL and Redis (one second each);
  `200` when both answer, else `503` with per-check status.
- **Counters** via stdlib `expvar` on `METRICS_ADDR`: HTTP requests by route
  and status class, appointments created, slot conflicts, emails sent and
  failed, tasks processed and failed by type, open WebSocket connections, rate
  limit rejections. CloudWatch on the Fargate target reads the logs instead.

## Security

**Rate limits** (fixed one-minute or one-hour windows in Redis, `429` with
`Retry-After`):

| Group | Key | Limit |
|---|---|---|
| `POST /auth/sessions` | visitor IP; email | 5 per minute; 10 per hour |
| `POST /public/appointments` | visitor IP | 5 per 10 minutes |
| `POST /public/contact-enquiries` | visitor IP | 5 per hour |
| `GET /public/availability` | visitor IP | 60 per minute |
| `/public/manage/*` | token hash | 20 per hour |
| `/public/sessions/*` | token hash | 30 per minute |
| other `/public/*` reads | service key | 1,200 per minute |
| `/admin/*`, `/auth/*` | session | 300 per minute |
| WebSocket upgrade | room id | 20 per minute |

The table is `internal/httpapi/ratelimit.go`, chosen by operation id; the
per-email sign-in limit is applied by the auth domain, because the email is in
the body, and the WebSocket handler calls `AllowRoomUpgrade` itself. Windows
start on a multiple of their length. Keys are
`rl:<group>:<first 32 hex of SHA-256(subject)>:<window start>`, so no address,
token or email is stored in Redis. Each check waits at most 250 ms. If Redis
does not answer, sign-in answers `503 unavailable`, because that is where a
missing limit helps an attacker most; every other group lets the request
through (ADR-006: losing Redis must not stop bookings) and logs
`rate_limit_unavailable` at most once a minute.

**Headers** on every response: `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`,
`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`,
`Cache-Control: no-store`. Caddy adds HSTS and terminates TLS.

**Input caps**: body caps as in the request lifecycle; visitor note 500
characters; enquiry message 5,000; names 120; WebSocket messages 16 KiB;
uploads 20 MiB, type sniffed from content, not the file name.

**Token formats**:

| Token | Form | Stored as | Lifetime |
|---|---|---|---|
| Session | `vms_` + base64url(32 random bytes) | SHA-256 | 12 h idle, 7 days absolute |
| Management link | base64url(HMAC-SHA256(`SIGNING_SECRET`, `"manage"` ‖ seed)), seed = 32 random bytes | seed + SHA-256 of token | Until the appointment is terminal and past |
| Video join | same construction with `"join"` | seed + SHA-256 of token | Start − 15 min to end + 60 min |
| Room ticket | base64url(payload) `.` base64url(HMAC-SHA256) | not stored | 5 minutes |
| Idempotency key | client UUID | as sent, with body hash | 24 hours |

A stored seed lets the worker render links; the database alone holds no usable token.

**Never logged**: names, email addresses, phone numbers, notes, enquiry text,
passwords, TOTP codes and secrets, any token or ticket, `Authorization` and
`X-Service-Key` headers, request and response bodies, query strings of token
routes, SDP and ICE payloads. Error logs carry codes and ids only.

## Local development and CI

- Go, PostgreSQL 17 and Redis from Homebrew (`brew services start`), no Docker
  on the laptop. Databases `vetmimi` and `vetmimi_test`; `DATABASE_URL_TEST`
  points at the second, and tests create throw-away databases beside it. Redis database 0 for development, 1 for tests.
- Media: any S3-compatible endpoint (MinIO from Homebrew works). Email: leave
  `RESEND_API_KEY` empty. Video: two browser tabs connect without TURN.
- `make dev` and `make worker` run the two modes; `make migrate` applies goose
  migrations (they also run at start-up). Tests use real PostgreSQL, never mocks.
- Heavy commands go through `scripts/gate.sh` (machine-wide lock; 8 GB laptop).
  `make gate` = lint, tests (`-p 1`), build, `generate-check`; CI runs the same
  with service containers for PostgreSQL and Redis, plus Conventional Commit and
  no-trailer checks.

## Deployment summary

As ADR-005: one small VPS runs Docker Compose with `api`, `worker` (same image,
`--mode worker`), `postgres:17`, `redis:7`, `caddy` and `coturn`. GitHub Actions
builds the image once per `main` commit, pushes it to GHCR tagged with the SHA,
pulls and restarts on the host, polls `/healthz` for 60 seconds and rolls back
automatically on failure. Nightly `pg_dump` goes to the bucket. The Fargate
topology lives in `deploy/terraform/`, validated in CI and never applied by
agents.

## Open points

- ADR-004 says nothing is stored as local wall-clock time; weekly rules are
  wall-clock by nature, so `availability_rules` alone stores local `time`.
- ADR-007 and `AGENTS.md` call link tokens random 32-byte values, but ADR-006
  tasks carry only a row id, so tokens are HMAC-derived from a random seed.
- ADR-008 gives each content table `featured`; ordered areas need
  `featured_slots`, so `featured` is maintained from the slots.
- ADR-008 lists `media` with the publication shape; media has no publication
  workflow and uses `asset_status` and `archived_at` instead.
- ADR-006 fixes `channel = 'email'`; "Mark as communicated" rows use `manual`.
- The content requirements name Professional Approver and Publisher roles;
  ADR-002 fixes three, so approving and publishing are `site_admin` rights.
- `AGENTS.md` calls durations and buffers settings rows; they belong to a
  service, so they are columns on `services`, editable like settings.
- The video hub lives in one api process; ADR-005's two-zone Fargate target
  would need sticky routing by room or Redis fan-out first.
