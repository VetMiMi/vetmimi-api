# Architecture

How the VetMiMi API is put together. Decisions and their reasons are in
`docs/adr/`; tables are in `docs/data-model.md`; how a route and its handler
are written is in `STYLE.md`. Where this document and an ADR disagree, the ADR wins.

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
| **api process** | `api --mode api` (the default). Gin router, all HTTP routes, the video signaling WebSocket. Stateless apart from the in-memory video room hub. |
| **worker process** | The same binary with `--mode worker`. Runs the asynq server: email delivery, hold expiry, scheduled publishing, social channel publishing, site revalidation, sweeps and clean-up. Never serves HTTP. |
| **PostgreSQL 17** | Every durable fact: settings, users, sessions, services, availability, appointments, communications, posts, media metadata. Enforces "no overlapping appointments" with an exclusion constraint (ADR-004). |
| **Redis 7 + asynq** | Task queues (`critical`, `default`, `low`) and rate-limit counters. Losing Redis loses only queued tasks; sweepers rebuild them from PostgreSQL (ADR-006). |
| **Resend** or **SMTP** | Transactional email, chosen by `EMAIL_PROVIDER`. Called only by the worker. |
| **S3-compatible bucket** | Media originals (private prefix `originals/`) and web sizes (public prefix `public/`), served from `MEDIA_PUBLIC_URL`. The API writes them during the upload request. |
| **coturn** | TURN relay on the live host for video sessions; the API mints time-limited credentials with the shared `TURN_SECRET` (ADR-007). |
| **Next.js site** (`vetmimi-next`) | The browser's only HTTP origin (ADR-002). Its server components and route handlers call this API server-side with `X-Service-Key` (public routes) or `Authorization: Bearer` (admin routes). The API has no CORS. |
| **WebSocket exception** | `GET /video/rooms/{roomId}/ws?ticket=…` is the one route a browser calls directly, with a five-minute room ticket that Next obtained server-side (ADR-007). |

The other entry point is `api --mode create-user`, which creates an
administrator, or replaces an existing one's password and TOTP secret. It reads
the password from standard input, never a flag, prints the `otpauth://` URI and
saves nothing until a valid code is typed back. With `--no-totp` it skips the
authenticator and saves a password-only account, clearing any existing secret.

## Package layout

One package per purpose under `internal/`, matching `AGENTS.md`. Domain packages
take `context.Context` and a `*pgxpool.Pool` or `db.Querier` and return typed
errors from `internal/apperr`. Only `internal/web`, each feature's handlers
(`handler.go` and `http_*.go`) and `cmd/api` serve HTTP; the rest of `meta`,
`linkedin`, `assistant` and `content` uses `net/http` only as a client of
outside APIs.

| Package | Owns |
|---|---|
| `config` | Configuration from the environment, read and checked once at start-up. |
| `postgres` | The pgx pool and the goose migrations. |
| `queue` | asynq: the `Task` domain functions return, the queue handlers enqueue them on, and the worker that runs task handlers and periodic tasks. |
| `ratelimit` | Fixed-window request counters in Redis. |
| `tokens` | Session tokens, management and join link tokens, OAuth state, booking and enquiry references. |
| `secretbox` | AES-256-GCM sealing of stored secrets: TOTP secrets and the Meta and LinkedIn tokens, under `TOTP_ENCRYPTION_KEY`. |
| `listing` | Keyset cursors and search patterns shared by the admin lists. |
| `apperr` | Typed errors with a stable code. |
| `clock` | The current time, injectable in tests. |
| `settings` | Typed read and write of the `settings` table. |
| `idempotency` | Stored responses under a create's `Idempotency-Key`. |
| `pgtest`, `redistest` | Test-only: a migrated database per test binary, and Redis clients. |
| `web` | What every route shares: `NewRouter` and the middleware every request gets (`web.go`); the route groups `Public`, `Admin`, `Slow` and `Root` (`NewRoutes`); `ServiceKey`, `SignedIn` and `Role` (`auth.go`); rate-limit rules (`ratelimit.go`); `BindJSON`, `BindQuery`, `BindURI` and `PathUUID` (`bind.go`); and the single `apperr` → problem mapping, `web.Error` (`errors.go`). |
| `webtest` | Test-only: serves a feature's routes behind the real middleware, on the test database and Redis, for handler tests. |
| `auth` | Users, password hashing (argon2id), TOTP enrolment and verification, sessions, roles, the sign-in throttle. |
| `booking` | Services, availability rules, overrides and blocks, slot generation (`slots.go`), appointment requests (`requests.go`), the status transition table (`status.go`), reschedules, the management link (`manage.go`), the admin dashboard (`dashboard.go`), hold expiry, reminders, retention purge, and their tasks (`worker.go`). |
| `video` | Video rooms, join tokens, room tickets, TURN credentials, the in-memory room hub and the WebSocket signaling loop, room closing. |
| `content` | The publishing portal (ADR-009): posts, their channel versions and publications, the workflow (`workflow.go`), approval checks per platform, public articles. |
| `media` | Uploads, type sniffing, web-sized derivatives, object storage, usage checks. |
| `meta` | The Facebook Page connection (Facebook Login, the sealed Page token, the linked Instagram account) and the Graph API calls that post the facebook and instagram channels (docs/meta-setup.md). |
| `linkedin` | The LinkedIn profile connection (OpenID Connect sign-in, the sealed member token and its expiry) and the Posts and Images API calls that post the linkedin channel (docs/linkedin-setup.md). |
| `assistant` | The portal's AI helper: suggests Facebook, Instagram and LinkedIn versions, and translates the website version, from the post's own text through the Anthropic Messages API; saves nothing. |
| `comms` | Communication records, templates (`comms/templates/<kind>.<locale>.tmpl`), delivery through Resend or SMTP, the sweeper, contact enquiries. |
| `db` | sqlc output (generated, committed) and `db/queries/*.sql`. |

Each feature package that serves HTTP has a `Handler` struct and a
`Routes(r web.Routes)` method in `handler.go`, listing every route with its
group, role and rate limit, and handlers in `http_*.go` with their request and
response structs written beside them. `runAPI` in `cmd/api/api.go` builds each
`Handler` and calls its `Routes`. Sign-in, sign-out, `/auth/me` and the health
routes live in `cmd/api` (`http_auth.go`, `http_health.go`), because
`internal/web` imports `internal/auth`.

Domain functions that cause background work return the tasks to enqueue; the
handler enqueues them **after** the transaction commits. Nothing enqueues from
inside a transaction.

## Request lifecycle

Middleware runs in this order:

1. **Request id** — reuse a valid incoming `X-Request-Id` or generate one; echo it in the response. The client IP is the last `X-Forwarded-For` hop, the one Caddy appended.
2. **Logger** — one slog line per request after it completes: request id, method, route pattern (never the raw path, so tokens in paths are never logged), status, duration, bytes.
3. **Recover** — a panic becomes `500 internal_error` and an error log with the stack.
4. **Security headers** (below).
5. **Group** — each route is registered on one group from `web.NewRoutes`:

   | Group | Timeout | Body cap | Caller | Group rate limit |
   |---|---|---|---|---|
   | `Public` | 30 s | 64 KiB | `X-Service-Key` | 1,200 a minute per service key |
   | `Admin` | 30 s | 512 KiB | `Authorization: Bearer <session>` | 300 a minute per session |
   | `Slow` (media upload, AI suggestions) | 2 min | 21 MiB | bearer session | 300 a minute per session |
   | `Root` (video WebSocket, `/healthz`, `/readyz`) | none | none | none; the WebSocket checks its own ticket | none |

   The timeout travels in the request context into every query; the body cap is `http.MaxBytesReader`.
6. **Route** — what the route lists in its feature's `Routes()`: the roles it was registered under (`r.Admin(roles...)`), then its own rate limit (`r.Limit`), if any.
7. **Handler** — binds the path, query and body into its own structs with `web.BindJSON`, `web.BindQuery`, `web.BindURI` or `web.PathUUID`, calls one domain function and writes JSON. A body, query or path that does not bind answers `400 invalid_request` with `errors[]`.

Request structs carry their rules as `binding` tags (`required`, `email`,
`oneof`, `max`, and the custom `slug`, `phone`, `hhmm` and `totp`). Unknown
JSON fields are refused. In `errors[]` a body field is named by its JSON
pointer and a query or path field by its name. Every message is built from
the rule, never from an error text, so a submitted value is never echoed.

### Error model

Every 4xx and 5xx body is an RFC 9457 `Problem` (`application/problem+json`):
`type` (`urn:vetmimi:problem:<code>`), `title`, `status`, `detail`, `code` and
`errors[]` of `{field, message}`. The site maps `code` to localised wording and
never shows `detail` to visitors. Renaming a code is a breaking change.

| Code | Status | Meaning |
|---|---|---|
| `invalid_request` | 400 | Body, query, header or path is malformed or breaks a field rule. |
| `unauthenticated` | 401 | Missing or wrong service key, or missing, expired or revoked session. |
| `invalid_credentials` | 401 | Sign-in failed. Same response for unknown email, wrong password and wrong TOTP code. |
| `forbidden` | 403 | Signed in, but no role grants this route. No resource data is returned. |
| `not_found` | 404 | Unknown id or slug, or an invalid or expired management or session token (neutral by design). |
| `slot_unavailable` | 409 | The requested time is no longer free. Carries `alternatives[]`. |
| `stale_version` | 409 | The `version` sent does not match the record; someone else changed it. Refresh and retry. |
| `invalid_transition` | 409 | The status change is not allowed from the current status. |
| `slug_taken` | 409 | Another record of the same type already uses the slug. |
| `overlapping_period` | 409 | A weekly availability period overlaps another on the same weekday. |
| `in_use` | 409 | Delete refused: media used by a post, or a service with appointments. Archive instead. |
| `payload_too_large` | 413 | Body or upload over its cap. |
| `unsupported_media_type` | 415 | Upload type not accepted (JPEG, PNG and WebP images only). |
| `outside_booking_window` | 422 | Start time inside the minimum notice or beyond the maximum advance window. |
| `service_not_bookable` | 422 | Service paused, archived, or its booking action is not `book` or `request`. |
| `booking_paused` | 422 | `public_booking_enabled` is false. |
| `acknowledgement_required` | 422 | Privacy or booking policy acknowledgement missing. |
| `idempotency_key_reused` | 422 | Same `Idempotency-Key` sent with a different body. |
| `action_not_allowed` | 422 | Business rule refuses the action, e.g. cancelling a past appointment through a management link, or a service or availability period that breaks a scheduling rule (`errors[]` names the field). |
| `publish_requirements_unmet` | 422 | A post cannot be approved: an enabled channel version breaks its platform's rules, no channel is enabled, or a True Story lacks consent; `errors[]` lists each one. |
| `rate_limited` | 429 | Too many requests for this route group; `Retry-After` is set. |
| `internal_error` | 500 | Unexpected failure. Logged with the request id; nothing was half-saved. |
| `ai_failed` | 502 | The AI assistant could not be reached or gave no usable answer; nothing was saved. Try again. |
| `unavailable` | 503 | PostgreSQL or Redis unreachable. |
| `feature_unavailable` | 503 | The feature is switched off on the server, e.g. the AI assistant without `ANTHROPIC_API_KEY`. |

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
deletes the row. An unknown email is checked against a fixed dummy argon2id
hash and a dummy TOTP secret, so every refused sign-in does the same work and
gets the same `401 invalid_credentials` body. The TOTP step claim,
`last_sign_in_at` and the session insert are one transaction. The bearer check
runs before the rate limiter, so callers without a live session are never
counted, and refreshes `last_seen_at` only when it is over a minute old.

**Passwords and TOTP.** argon2id (64 MiB, 3 iterations, parallelism 2, PHC
string). TOTP (30 s, 6 digits, ±1 step) is enrolled by `api --mode
create-user`, so the API has no enrolment routes. Until two-step setup moves
into the admin website (#142), a user may have no secret (`--no-totp`): they
sign in with the password alone, any code sent is ignored, and
`twoStepEnabled` is false on `/auth/me`; wrong passwords, lockout and rate
limits are unchanged and give the same errors. Secrets are
encrypted with `TOTP_ENCRYPTION_KEY` (AES-256-GCM); the last accepted step is
stored against replay. Ten failures for one email in 15 minutes lock it until
15 minutes after the tenth, whether or not the email has an account; a
success leaves the failures to expire, so it reveals no right guess. The
lockout and the email's hourly limit are checked before any password is
hashed, and each refusal logs `sign_in_locked` with a 12-character SHA-256
prefix of the email.

**Roles** (ADR-002). One user may hold several. `site_admin` implies the other two.

| Route group | content_editor | booking_admin | site_admin |
|---|---|---|---|
| `/auth/me`, sign-out | yes | yes | yes |
| `GET /admin/settings` | `site` keys only | yes | yes |
| `PATCH /admin/settings` | no | `booking` keys | all keys |
| `/admin/services`, `/admin/availability/*` | no | yes | yes |
| `/admin/appointments/*` (video session start and end, resend and mark as communicated included), `/admin/dashboard` | no | yes | yes |
| `/admin/contact-enquiries` | no | yes | yes |
| `/admin/posts`: list, get, create, patch, delete, submit, mark a channel posted | yes | no | yes |
| `/admin/posts/{id}`: request-changes, approve, schedule, unschedule, publish, archive | no | no | yes |
| `/admin/posts/{id}/suggestions`, `GET /admin/ai/status` | yes | no | yes |
| `/admin/connections/meta`: read, Facebook Login, choose the Page, disconnect | no | no | yes |
| `/admin/connections/linkedin`: read, sign in, disconnect | no | no | yes |

Each admin route names its roles in its feature's `Routes()`, as in
`r.Admin(auth.BookingAdmin)`, so the table above can be read off the
`handler.go` files. `Admin` and `Slow` take the roles as arguments, so an admin
route cannot be registered without them. The role check runs after the
session check and the session's rate limit, and before the route's own limit
and binding. Every refusal is the same `403 forbidden` body, naming no route or
resource. Rules finer than a route, the settings key groups, are the domain's:
it reads the roles with `auth.FromContext`.

Content editors never see booking data; booking administrators never edit
content. Daw Mi holds all three roles.

## Sequence walkthroughs

### 1. Public booking request submit

1. The browser generates a UUID idempotency key when the review step mounts,
   kept across double-clicks and refreshes.
2. Next calls `POST /public/appointments` with `X-Service-Key`, `X-Visitor-IP`
   and `Idempotency-Key`; middleware rate-limits and validates the body.
3. `booking.RequestAppointment` opens a transaction and inserts an
   `idempotency_keys` row (scope `public_appointment`, SHA-256 of the body as
   decoded and re-encoded, so spacing and key order do not matter). If
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
    `timezone`. A replay answers the stored `201` with `Idempotent-Replayed: true`. Next shows "Appointment request received — Status: Pending".

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
   start − 15 minutes, `closes_at` = end + 60 minutes. `manual_link` mode is not
   served yet: confirm accepts `meetingLink` and ignores it.
6. Events row `confirmed`; communications `booking_confirmed` (visitor) and
   `reminder` (visitor, `scheduled_for` = start − `reminder_hours`, only if that
   is still in the future).
7. Commit. Enqueue `comms:deliver` for the confirmation now and for the
   reminder at `scheduled_for`; delete the `hold:<id>` task (if it fires anyway it
   re-checks status and does nothing).
8. The worker renders the confirmation in the visitor's locale, re-derives the
   join link from the room's seed, sends through Resend (idempotency key = row
   id) and records `sent`, or retries 5 times and records `failed`. A failed
   email never reverts the confirmation; the dashboard lists it under
   attention as `failed_communication`, where Daw Mi can resend it or mark it
   as communicated.

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
5. When Daw Mi changes `reminder_hours`, `comms:reschedule-reminders` moves every
   `queued` reminder to `start − reminder_hours` and replaces its task, so each
   confirmed appointment still gets exactly one reminder at the new offset. A
   task still holding an old, earlier time finds the row not yet due and leaves
   it.

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
   handler (on the `Root` group: no timeout, body cap or sign-in) requires `Origin` =
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
   The worker cannot reach the api's hub, so the hub reads its open rooms back
   every 30 seconds and closes those ended or past `closes_at` with 4002; it
   pings every 20 seconds and drops a participant silent for 10 more, and on
   shutdown closes every socket with 1001 so the browser reconnects.

### 5. Publish a post

1. An editor writes the post and its channel versions (`POST`/`PATCH /admin/posts`)
   and submits it; the post is `in_review`.
2. Daw Mi approves (`site_admin`). In one transaction the post is locked, its
   `version` checked, and every enabled version checked against its platform
   (docs/data-model.md, "Posts"); a True Story needs consent confirmed. Failures
   give `422 publish_requirements_unmet` listing each one.
3. She publishes now, or schedules a future time (`content:publish-scheduled`
   does the same at that time). Publishing opens a `post_publications` row per
   enabled channel: the website's is `published` at once and the website
   version is copied to `published_articles`, which `GET /public/articles/{slug}`
   serves, and `content:revalidate` asks the site to refresh it; social ones are
   `pending` and the post is `publishing`.
4. Each social channel is its own `content:publish-channel` task, calling that
   platform's connector: `internal/meta` for Facebook and Instagram once the
   Page is connected, `internal/linkedin` for LinkedIn once her profile is,
   otherwise `not_connected`. A transient failure is retried three times. An attempt
   whose answer is lost on the call that posts, or that is still `publishing`
   after 10 minutes (its worker died), fails as `unknown_outcome` and is never
   sent again by itself, since it may have posted. A failed channel can be
   retried, or Daw Mi posts it herself (copy & open) and marks it posted. When
   every channel is `published` or `manual`, the post is `published`.
5. Editing an `approved` or `scheduled` post sends it back to `in_review`; so
   does editing a published post's website version, while visitors keep
   reading the copy in `published_articles` until it is published again.

## Time handling rules

- Every instant is `timestamptz` and every JSON timestamp is RFC 3339 in UTC
  (`Z`). Responses about appointments and availability also carry `timezone`.
- The practice timezone is `settings.timezone` (`Australia/Sydney`), applied
  with `time.LoadLocation`. Nothing adds or subtracts hours to convert.
- Weekly availability rules are the one wall-clock value: weekday plus local
  start and end time. Slot generation expands them per local date with
  `time.Date(…, loc)`. One-off openings, blocks and appointments are instants.
- Slots are generated by stepping `slot_step_minutes` in real time through each
  open period (placed on its date with `time.Date(…, loc)`), so the October gap
  never yields a start, and keeping each wall-clock time of the repeated April
  hour once, at its first instant. A candidate's busy range (session plus both
  buffers) must lie inside the period and miss every block and every pending or
  confirmed busy range, exactly as the exclusion constraint will judge it, so
  neighbouring buffers do not overlap. Tests cover both Sydney transition Sundays.
- Public availability takes `from` and `to` as local dates in the practice
  timezone; the response lists slot instants in UTC.
- Manual bookings use the same slots and window as the public site. A
  reschedule must land on a free slot too (its own old time counting as free),
  but Daw Mi may move an appointment inside `min_notice_hours` or past
  `max_advance_days`; only a start in the past is refused.
- Durations and offsets (`reminder_hours`, `pending_hold_hours`,
  `min_notice_hours`) are absolute hours, so a 24-hour reminder is 24 real hours
  even across a daylight-saving change.
- Dashboard "today" uses the local day. Emails show local time with AEST or
  AEDT. All "now" comes from `internal/clock`, so tests can fix it.

## Background jobs

| Task | Trigger | Schedule | Idempotency |
|---|---|---|---|
| `comms:deliver` | After commit of any communication row; reminder rows at `scheduled_for` | Immediate or `ProcessAt` | Task id `comms:<id>`; worker locks the row and skips unless `queued`; Resend idempotency key = row id |
| `comms:sweep` | asynq periodic | Every 5 minutes | Enqueues `queued` rows due more than a minute ago with the same task ids |
| `comms:reschedule-reminders` | After `updateSettings` changes `reminder_hours` | Immediate | Task id `reminders:<settings version>`; rewrites only `queued` reminder rows and replaces their tasks |
| `booking:expire-hold` | Appointment created as `pending` | At `hold_expires_at` | Task id `hold:<id>`; acts only if still `pending` and the hold has passed; writes `expired`, event and `request_expired` email in one transaction |
| `booking:sweep-holds` | asynq periodic | Every 5 minutes | Runs the same expiry for any overdue `pending` row |
| `content:publish-scheduled` | `schedule` action | At `scheduled_at` | Task id `post:<id>:<scheduled_at>`; runs the same start of publishing as Publish now, only if the post is still `scheduled` and due |
| `content:publish-channel` | Publishing starts; `retry` action | Immediate | Task id `publish:<post>:<channel>:<attempts>`; claims the row only while `pending` and the post `publishing`, never one already `publishing`; up to 4 attempts, then `failed` |
| `content:revalidate` | An article goes live, goes live again after an edit, or is archived | Immediate | `POST {SITE_URL}/api/revalidate` with `X-Revalidate-Secret`, tags `articles` and `article:<slug>`; skipped without `SITE_REVALIDATE_SECRET`; revalidating twice is harmless |
| `content:sweep` | asynq periodic | Every 5 minutes | Re-enqueues scheduled posts past due and channels `pending` for over 10 minutes, with the same task ids; fails channels `publishing` for over 10 minutes as `unknown_outcome` |
| `video:close-room` | Room created or moved | At `closes_at` | Task id `room:<id>:<closes_at>`; ends the room only if still open and the window has passed |
| `video:sweep-rooms` | asynq periodic | Every 5 minutes | Ends only rooms still open past `closes_at`; ending twice is a no-op |
| `booking:purge-retention` | asynq periodic | Daily 03:00 practice time (`CRON_TZ`, from `settings.timezone` when the worker starts) | Deletes final appointments and contact enquiries past `retention_months`, 500 rows a statement; deleting twice deletes nothing |
| `platform:cleanup` | asynq periodic | Hourly | Deletes expired sessions and idempotency keys older than 24 hours |

## Configuration

Read once at start-up by `internal/config`; a missing required variable stops the
process with a clear error. `.env.example` lists exactly these, with empty
values for secrets.

| Variable | Purpose |
|---|---|
| `PORT` | HTTP listen port (default 8080). |
| `ENV` | `development` or `production`. Production refuses to start without every secret. |
| `LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`. |
| `DATABASE_URL` | PostgreSQL connection string. |
| `DATABASE_URL_TEST` | Server for `go test`; each test binary creates and drops its own `vetmimi_test_<random>` database on it (`internal/pgtest`). |
| `REDIS_URL` | Redis for asynq and rate limits. |
| `REDIS_URL_TEST` | Redis database used by tests (`/1`). Each test uses queue names and a rate-limit key prefix of its own and deletes only its own keys, never `FLUSHDB`, because test binaries share the database; a missing value fails the run. |
| `SERVICE_KEY` | Shared with the Next server; required on public routes and sign-in. |
| `SIGNING_SECRET` | 32+ random bytes; HMAC key for management and join tokens and room tickets. |
| `TOTP_ENCRYPTION_KEY` | 32 bytes, base64; encrypts TOTP secrets at rest. |
| `PUBLIC_API_URL` | The API's public origin, used to build the `wss://` URL in room tickets. |
| `SITE_URL` | The public site, for links in emails, the WebSocket `Origin` check and revalidation. |
| `SITE_REVALIDATE_SECRET` | Shared with the site's `POST /api/revalidate`. |
| `EMAIL_PROVIDER` | `resend` (default; needs a verified domain) or `smtp` (e.g. Gmail with an app password). |
| `RESEND_API_KEY` | Resend API key, required in production when the provider is `resend`. Empty in development: emails are logged as "would send" (kind and id only). |
| `EMAIL_FROM` | Daw Mi's approved sender, e.g. `VetMiMi <hello@…>`; with Gmail, the Gmail address. Reply-to is `settings.contact_email`. |
| `SMTP_HOST`, `SMTP_PORT` | Mail server when the provider is `smtp`, e.g. `smtp.gmail.com` and `587` (STARTTLS). |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | Sign-in for the mail server; for Gmail, the address and an app password. |
| `MEDIA_S3_ENDPOINT` | S3-compatible endpoint. |
| `MEDIA_S3_REGION` | Region name the provider expects. |
| `MEDIA_S3_BUCKET` | Bucket for originals and derivatives. |
| `MEDIA_S3_ACCESS_KEY`, `MEDIA_S3_SECRET_KEY` | Bucket keys; set together. Empty on AWS, where the EC2 instance role (`deploy/terraform/live/`) or the ECS task role (`deploy/terraform/demo/`, found through `AWS_CONTAINER_CREDENTIALS_RELATIVE_URI`) grants the bucket. |
| `MEDIA_PUBLIC_URL` | Base URL the bucket's `public/` prefix is served from. |
| `TURN_HOST` | coturn `host:port`. Empty in development: room tickets offer STUN only. |
| `TURN_SECRET` | coturn `static-auth-secret` for time-limited credentials. |
| `META_APP_ID`, `META_APP_SECRET` | The Meta app that publishes to Daw Mi's Facebook Page and Instagram (docs/meta-setup.md); set together. Empty: those channels are posted by hand. |
| `META_CONFIG_ID` | The Facebook Login for Business configuration naming the permissions; empty, the dialog asks for them as scopes. |
| `META_GRAPH_VERSION` | Graph API version the connector calls (default `v26.0`). |
| `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET` | The LinkedIn app that publishes to Daw Mi's profile (docs/linkedin-setup.md); set together. Empty: LinkedIn is posted by hand. |
| `LINKEDIN_API_VERSION` | `LinkedIn-Version` header, `YYYYMM` (default `202609`); LinkedIn supports each version for at least a year. |
| `ANTHROPIC_API_KEY` | The portal's AI assistant (`POST /admin/posts/{id}/suggestions`). Empty: the assistant answers `503 feature_unavailable`. |
| `ANTHROPIC_MODEL` | Model the assistant asks (default `claude-haiku-4-5-20251001`). |
| `METRICS_ADDR` | Optional `host:port`, checked at start-up. Reserved for serving the `expvar` counters; nothing listens on it yet. |

## Observability

- **Logs**: slog JSON to stdout with `request_id` (HTTP) or `task` and
  `task_id` (worker). Domain logs name ids, never people. The request id is
  echoed in `X-Request-Id`, so an administrator's report can be traced.
- **`/healthz`** (liveness): returns `200 {"status":"ok"}` if the process can
  serve; it touches no dependency. The deploy rollback (ADR-005) polls it.
- **`/readyz`** (readiness): pings PostgreSQL and Redis (one second each);
  `200` when both answer, else `503` with per-check status.
- **Counters** are registered with stdlib `expvar`: appointments created, slot
  conflicts, appointments and enquiries purged, emails failed. Nothing serves
  them yet: `METRICS_ADDR` is read and checked but no listener uses it.

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
| `POST /admin/posts/{id}/suggestions` | user | 20 per hour |
| `/admin/*`, `/auth/*` | session | 300 per minute |
| WebSocket upgrade | room id | 20 per minute |

Each rule is a `web.Rule` beside the routes that use it, in their feature's
`handler.go` (sign-in's in `cmd/api/http_auth.go`); the two group limits are
in `internal/web/ratelimit.go`. The per-email sign-in limit is applied by
the auth domain, because the email is in the body. The WebSocket route counts
by room id after checking the request (`checkRoomRequest`). Windows start on a multiple of their length. Keys are
`rl:<group>:<first 32 hex of SHA-256(subject)>:<window start>`, so no address,
token or email is stored in Redis; the lockout keeps
`lockout:failures:<hash>` (a sorted set of failure times) and
`lockout:until:<hash>`, hashed the same way and expiring on their own. Each check waits at most 250 ms. If Redis
does not answer, sign-in answers `503 unavailable`, at the visitor-IP limit
and in the auth domain alike, because that is where a missing limit helps an
attacker most; every other group lets the request
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

As ADR-010 (which updates ADR-005): one EC2 `t4g.small` in Sydney runs Docker
Compose with `api`, `worker` (same image, `--mode worker`), `web` (the
`vetmimi-next` image), `postgres:17`, `redis:7`, `caddy` and `coturn`. After CI
passes on `main`, GitHub Actions builds the arm64 image, pushes it to ECR
tagged with the SHA (through an OIDC role, no stored key), and runs
`deploy/deploy.sh` on the host, which polls the public `/healthz` for a minute
and rolls back on failure. Nightly `pg_dump` goes to a private S3 bucket, and
CloudWatch alarms email the owner. `deploy/README.md` is the runbook. The
Terraform roots are `deploy/terraform/live` and `deploy/terraform/budget`;
`deploy/terraform/demo` is ADR-005's Fargate topology, applied only for a
one-day demo.

## Open points

- ADR-004 says nothing is stored as local wall-clock time; weekly rules are
  wall-clock by nature, so `availability_rules` alone stores local `time`.
- ADR-007 calls link tokens random 32-byte values, but ADR-006
  tasks carry only a row id, so tokens are HMAC-derived from a random seed.
- ADR-006 fixes `channel = 'email'`; "Mark as communicated" rows use `manual`.
- The content requirements name Professional Approver and Publisher roles;
  ADR-002 fixes three, so approving and publishing are `site_admin` rights.
- The video hub lives in one api process; ADR-005's two-zone Fargate target
  would need sticky routing by room or Redis fan-out first.
