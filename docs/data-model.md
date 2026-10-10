# Data model

The launch tables in migration order. Decisions are in `docs/adr/`; a migration updates this file in the same PR.

## Conventions

- Primary keys are `uuid DEFAULT gen_random_uuid()` unless stated; append-only logs use `bigint` identity.
- Every instant is `timestamptz`. The only wall-clock values are the weekday and times in `availability_rules`.
- Status and kind columns are `text` with `CHECK (col IN (…))`, not PostgreSQL enums, so adding a value is one
  migration line. Allowed transitions live in Go (`internal/booking/status.go`, `internal/content/workflow.go`).
- `localized` is a domain over `jsonb`: an object whose only keys are `en` and `my`. **en req.** adds
  `CHECK (col ? 'en')`. Public reads fall back to `en`. Article bodies are `localized` Markdown.
- `…_by` columns are `uuid`
  FK users `ON DELETE SET NULL`. Emails are stored lower-case; slugs match `^[a-z0-9]+(-[a-z0-9]+)*$`.
- Admin-edited rows carry `version int DEFAULT 1`; writes send the version they read; a mismatch is
  `409 stale_version`. `created_at`/`updated_at` (`now()`) are on every table and listed only where notable.

## settings

Business rules Daw Mi may change without a deploy, one row per key (see "Settings keys"), seeded with defaults.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| key | text | no | — | PK; unknown keys are refused by the API |
| value | jsonb | no | — | typed and range-checked per key in `platform/settings` |
| updated_by, updated_at | uuid, timestamptz | yes, no | —, now() | a patch lands whole or not at all |

## Accounts

**`users`** — Administrators. Daw Mi is the one practitioner.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| email, display_name | text | no | — | email unique |
| password_hash | text | no | — | argon2id PHC string |
| roles | text[] | no | — | subset of `content_editor`, `booking_admin`, `site_admin`; at least one |
| is_practitioner | boolean | no | false | the person appointments are booked with |
| totp_secret_enc, totp_last_step | bytea, bigint | yes, yes | — | AES-256-GCM ciphertext, NULL for a password-only user (`--no-totp`); last accepted step (replay guard), set to the enrolment code's step by create-user |
| last_sign_in_at, disabled_at | timestamptz | yes | — | disabled users cannot sign in |

Constraints: `CHECK (roles <@ ARRAY[…] AND cardinality(roles) > 0)`; unique `email` and
`CHECK (email = lower(email))`; unique partial index on `is_practitioner WHERE is_practitioner`. TOTP is columns,
not a `totp_secrets` table: each user has exactly one secret and nothing else refers to it, so a table would only
add a join.

**`sessions`** — Admin sign-in sessions (ADR-002).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| user_id, token_hash | uuid, bytea | no | — | FK users `CASCADE`; SHA-256 of the token, unique |
| last_seen_at | timestamptz | no | now() | idle expiry = `last_seen_at + 12 h` |
| expires_at | timestamptz | no | — | absolute expiry = sign-in + 7 days |

Indexes: unique `token_hash`; `user_id`; `expires_at`. No `updated_at`: a session is never edited, and
`last_seen_at` (refreshed at most once a minute) is the one column that changes. Re-running create-user deletes the
user's sessions in the transaction that replaces the password; `platform:cleanup` deletes expired rows hourly.

## services

What can be booked or enquired about, with its scheduling facts. Editorial copy stays in the site's code.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| slug | text | no | — | unique; also the service page slug |
| name | localized | no | — | **en req.**; one name for booking, emails and the page |
| description | localized | yes | — | short text on the booking service step |
| booking_action | text | no | `'request'` | `book`, `request`, `enquiry_only`, `not_bookable` |
| state | text | no | `'active'` | `active`, `paused`, `archived` |
| duration_minutes | int | yes | — | 5–480; required when action is `book` or `request` |
| buffer_before_minutes, buffer_after_minutes | int | no | 0 | 0–240 each |
| formats | text[] | no | `'{online}'` | subset of `online`, `in_person` |
| fee_text, preparation_text | localized | yes | — | fee wording; preparation notes for the confirmation |
| sort_order, version | int | no | 0, 1 | |

Constraint: `CHECK (booking_action NOT IN ('book','request') OR duration_minutes IS NOT NULL)`; unique `slug`.
State: `active ⇄ paused`, `active|paused → archived`, `archived → paused`. Publicly bookable = `active`, action
`book`/`request`, and `public_booking_enabled`. Seeds, all online: `individual-art-therapy` (request, 60 min,
0/15 buffers), `free-consultation` (request, 20 min), `group-art-wellbeing`, `workshops-programs` (enquiry_only).
`free-consultation` is seeded `paused`: neither the site nor the requirements mention it, so it stays unbookable
until Daw Mi confirms it and resumes it from admin.

## Availability

**`availability_rules`** — Recurring weekly hours; several periods per weekday.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| weekday | smallint | no | — | ISO 1 (Monday) to 7 (Sunday) |
| start_time, end_time | time | no | — | local practice time; `end_time > start_time` |

Constraint `availability_rules_no_overlap`: `EXCLUDE USING gist (weekday WITH =, tsrange(date '2000-01-01' +
start_time, date '2000-01-01' + end_time) WITH &&)` → `409 overlapping_period`.

**`availability_overrides`** — One-off openings and date-specific changes to the weekly hours.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| on_date | date | no | — | local practice date affected |
| kind | text | no | — | `open` adds time; `replace` rows together replace that date's weekly periods |
| period | tstzrange | no | — | `[)`, non-empty, inside `on_date` in the practice timezone (checked in Go) |
| note, created_by | text, uuid | yes | — | private, ≤ 500 |

Index: `on_date`. Closing a whole date is a block, not an override.

**`availability_blocks`** — Time Daw Mi is unavailable: part of a day, whole days, or a date range.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| period | tstzrange | no | — | `[)`, non-empty |
| all_day | boolean | no | false | created as whole local days; display only |
| reason, created_by | text, uuid | yes | — | private, never returned by public routes, ≤ 500 |

Index: GiST `period`. A block overlapping pending or confirmed appointments is saved and the response lists them
as `conflicts`; nothing is cancelled automatically. Every availability write (rules, overrides, blocks) and
appointment creation share one advisory lock, `pg_advisory_xact_lock(hashtext('availability:' || practitioner_id))`.

## Appointments

**`appointments`** — One row per request or booking, whatever happens to it (ADR-004).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| reference | text | no | — | unique; `VM-` + 6 random Crockford base32 characters; the id people see |
| practitioner_id, service_id | uuid | no | — | FK users, FK services, both `RESTRICT` |
| status | text | no | `'pending'` | see transitions |
| starts_at, ends_at | timestamptz | no | — | session only; `ends_at > starts_at` |
| duration_minutes | int | no | — | as booked |
| busy_range | tstzrange | no | — | `[starts_at − buffer before, ends_at + buffer after)`, computed at write time |
| timezone | text | no | — | practice timezone when booked |
| format, locale, source | text | no | —, `'en'`, `'website'` | `online`/`in_person`; `en`/`my` (language of every email); `website`/`manual` |
| visitor_name, visitor_email | text | no | — | 1–120; ≤ 254 |
| visitor_phone, visitor_note | text | yes | — | ≤ 32; practical note ≤ 500 |
| privacy_ack_at, policy_ack_at | timestamptz | yes | — | required when `source = 'website'` |
| hold_expires_at | timestamptz | yes | — | set while `pending` |
| management_token_seed, management_token_hash | bytea | no | — | 32 random bytes; SHA-256 of the derived token (unique) |
| meeting_link, admin_note | text | yes | — | `https://` only, `manual_link` mode; private scheduling note ≤ 2,000 |
| late_cancellation | boolean | no | false | cancelled inside `cancellation_notice_hours` |
| status_changed_at | timestamptz | no | now() | |
| created_by, version | uuid, int | yes, no | —, 1 | `created_by` set for manual bookings |

Constraints: `CHECK (busy_range @> tstzrange(starts_at, ends_at))`, `CHECK (hold_expires_at IS NULL OR status IN
('pending','expired'))`, `CHECK (source = 'manual' OR (privacy_ack_at IS NOT NULL AND policy_ack_at IS NOT NULL))`, and:

```sql
ALTER TABLE appointments ADD CONSTRAINT appointments_no_overlap
  EXCLUDE USING gist (practitioner_id WITH =, busy_range WITH &&)
  WHERE (status IN ('pending', 'confirmed'));
```

Indexes: unique `reference`, unique `management_token_hash`, `(status, starts_at)`, `(service_id, starts_at)`,
`visitor_email`, `hold_expires_at WHERE status = 'pending'`. Name search is `ILIKE` over a few hundred rows a year.

Transitions: `pending → confirmed | declined` (admin), `pending → expired` (hold job), `pending|confirmed →
cancelled_by_client` (management link), `confirmed → cancelled_by_practitioner` (admin), `confirmed → completed |
no_show` (admin, after `starts_at`). All other statuses are final. Rescheduling is not a status: one `UPDATE` moves `starts_at`, `ends_at` and
`busy_range` (the constraint secures the new range before the old is released) and writes a `rescheduled` event.
Manual bookings use the same table, constraint and transitions and are created `confirmed`.

**`appointment_events`** — Appointment history; append-only. `id bigint` identity.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| appointment_id | uuid | no | — | FK appointments `CASCADE` |
| kind | text | no | — | `created`, `confirmed`, `declined`, `rescheduled`, `reschedule_requested`, `cancelled`, `completed`, `no_show`, `expired`, `note_updated`, `meeting_link_set` |
| from_status, to_status | text | yes | — | for status changes |
| previous_range, new_range | tstzrange | yes | — | session times for reschedules |
| actor, actor_user_id | text, uuid | no, yes | — | `visitor`, `admin`, `system` |
| detail | jsonb | no | `'{}'` | non-personal facts, e.g. `{"late_cancellation": true}`; never note text |

Index: `(appointment_id, created_at)`.

**`video_rooms`** — A VetMiMi video room for one confirmed online appointment (ADR-007).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| appointment_id | uuid | no | — | FK appointments `CASCADE`; unique |
| join_token_seed, join_token_hash | bytea | no | — | 32 random bytes; SHA-256 of the derived token (unique) |
| opens_at, closes_at | timestamptz | no | — | `starts_at − 15 min`, `ends_at + 60 min`; moved on reschedule |
| state | text | no | `'waiting'` | `waiting`, `in_session`, `ended` |
| started_at, ended_at, ended_reason | | yes | — | reason: `practitioner`, `window_closed`, `appointment_cancelled` |

Transitions: `waiting → in_session` (both peers connected); `waiting|in_session → ended`. Room tickets are signed
tokens, not rows: they live five minutes, are checked once at upgrade, and all they carry fits in the token.

## Enquiries, communications and idempotency

**`contact_enquiries`** — Messages from the contact form and enquiry-only services.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| reference | text | no | — | unique, `EN-` + 6 characters |
| name, email | text | no | — | 1–120; ≤ 254 |
| organisation, subject | text | yes | — | ≤ 200 each |
| enquiry_type | text | no | — | `collaboration`, `workshop`, `speaking`, `art_of_wellness`, `media`, `organisation`, `general` |
| service_id | uuid | yes | — | FK services `SET NULL`; when sent from a service page |
| message | text | no | — | 1–5,000 |
| locale, privacy_ack_at | text, timestamptz | no | `'en'`, — | |
| status, handled_at, handled_by | | no, yes, yes | `'new'` | `new ⇄ handled` |

Index: `(status, created_at)`.

**`communications`** — Every message sent or recorded, written in the transaction that caused it (ADR-006).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| id | uuid | no | gen_random_uuid() | also the asynq task id and the Resend idempotency key |
| appointment_id, contact_enquiry_id | uuid | yes | — | FK `CASCADE`; exactly one is set |
| kind | text | no | — | see below |
| audience | text | no | — | `visitor`, `practitioner` |
| channel | text | no | `'email'` | `email`, or `manual` for Mark as communicated |
| recipient | text | yes | — | email address; null for `manual` |
| locale | text | no | — | template locale |
| status | text | no | `'queued'` | `queued`, `sent`, `failed`, `cancelled` |
| scheduled_for | timestamptz | no | now() | reminders are in the future |
| sent_at, provider_message_id, error | timestamptz, text, text | yes | — | error = provider error or skip reason, no personal data |
| attempts | int | no | 0 | |
| resend_of, created_by | uuid | yes | — | FK communications, set by Resend; author of manual rows |
| note | text | yes | — | `manual` only: how it was communicated, private |
| message | text | yes | — | Own words, rendered as written (≤ 1,000): Daw Mi's to the visitor on a decline or cancellation, or the visitor's to her on a cancellation or reschedule request through the management link |

Kinds: `request_received`, `booking_confirmed`, `request_declined`, `rescheduled`, `cancelled`, `reminder`,
`request_expired` (visitor); `practitioner_new_request`, `practitioner_new_booking`,
`practitioner_client_cancelled`, `practitioner_reschedule_requested`, `practitioner_new_enquiry` (practitioner).
Constraints: `CHECK (num_nonnulls(appointment_id, contact_enquiry_id) = 1)`, `CHECK (status <> 'sent' OR sent_at
IS NOT NULL)`, `CHECK (channel = 'manual' OR recipient IS NOT NULL)`, `recipient` lower-case, `note` on `manual`
rows only. Transitions (`internal/comms/comms.go`): `queued → sent | failed | cancelled`, all final; Resend and
Mark as communicated insert new rows. Indexes: `(appointment_id, created_at)`, `scheduled_for WHERE status =
'queued'`, `created_at WHERE status = 'failed'`.

**`idempotency_keys`** — Stored responses for retried creates (ADR-004), kept 24 hours.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| scope | text | no | — | `public_appointment`, `admin_appointment`, `contact_enquiry` |
| key | text | no | — | as sent in `Idempotency-Key`, ≤ 128 |
| request_hash | bytea | no | — | SHA-256 of the request body |
| resource_id, response_status, response_body | uuid, smallint, jsonb | yes | — | the row created and the response sent |
| created_at | timestamptz | no | now() | |

PK `(scope, key)`; index `created_at`. Inserted in the creating transaction, so a failed create leaves no key.

## Posts

The publishing portal (ADR-009): Daw Mi writes one post and a version of it for each channel. The website pages
(Home, About, Services, Portfolio, Art of Wellness) are not posts and stay in the site's code.

**`posts`**

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| title | text | no | — | working title in the portal, 1–200 |
| kind | text | no | — | `insight`, `true_story`, `announcement` |
| status | text | no | `'draft'` | `idea`, `draft`, `in_review`, `approved`, `scheduled`, `publishing`, `published`, `archived` |
| scheduled_at | timestamptz | yes | — | required while `scheduled` |
| consent_confirmed_at, consent_confirmed_by, consent_note | | yes | — | True Story consent; the note (≤ 2,000) says where it is kept, internal |
| review_note | text | yes | — | the reviewer's note from the last request for changes, ≤ 2,000 |
| author_id, approved_at, approved_by | | yes | — | |
| published_at | timestamptz | yes | — | when every enabled channel was published |

Index: `(status, created_at)`. Transitions (`internal/content/workflow.go`): `idea ⇄ draft` (an edit),
`idea|draft → in_review` (submit), `in_review → draft` (request changes, with a note), `in_review → approved`
(approve), `approved|scheduled → scheduled` (schedule, future time), `scheduled → approved` (unschedule),
`approved|scheduled → publishing|published` (publish now; the scheduler will do the same at `scheduled_at`),
`publishing → published` (once every publication is `published` or `manual`), any → `archived`. Saving an
`approved` or `scheduled` post moves it back to `in_review`. A `publishing` or `published` post may change only
its title and website version; that too returns it to `in_review` (visitors keep reading the article as last
published until it is published again, which republishes the website only). Approval needs at least one enabled channel, every enabled version
valid for its platform, every image it names in `media`, and, for a True Story, consent confirmed. Only ideas and drafts that were never published may be deleted.

**`post_versions`** — One row per post and channel. PK `(post_id, channel)`; FK posts `CASCADE`.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| channel | text | no | — | `website`, `facebook`, `instagram`, `linkedin` |
| enabled | boolean | no | false | only enabled channels are checked and published |
| slug | text | yes | — | website; unique among website versions (`409 slug_taken`) |
| title, excerpt, body, seo_title, seo_description | localized | yes | — | website; body is Markdown stored as written, rendered safely by the site |
| cover_image_id | uuid | yes | — | website |
| text | text | yes | — | Facebook and LinkedIn text, Instagram caption |
| link_url | text | yes | — | Facebook and LinkedIn |
| image_ids | uuid[] | no | `'{}'` | social images in order: Facebook ≤ 10, Instagram 1–10, LinkedIn ≤ 1 |

`post_versions_fields_by_channel` keeps website fields on the website row and social fields on the others.
Image ids are `media` ids, checked at approval rather than by foreign key (`image_ids` is an array).
Approval checks: website — slug and English
title, excerpt and body; Facebook — text; Instagram — at least one image, caption ≤ 2,200 characters and
≤ 30 hashtags; LinkedIn — text ≤ 3,000 characters. Drafts may exceed these.

**`post_publications`** — How each enabled channel's publishing went, created when publishing starts. PK
`(post_id, channel)`; FK posts `CASCADE`.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| channel | text | no | — | as in `post_versions` |
| status | text | no | `'pending'` | `pending`, `publishing`, `published`, `failed`, `manual` (posted by hand: copy & open) |
| external_id, permalink, error | text | yes | — | the platform's id and address; the failure reason: `not_connected`, `connection_failed`, `rejected` (the platform refused it), `reconnect_required`, `unknown_outcome` (may have posted; never re-sent by itself) |
| attempts | int | no | 0 | worker attempts this round: a first try and three retries, reset by `retry` |
| published_at | timestamptz | yes | — | required when `published` or `manual` |

The website publication is `published` as soon as publishing starts; social ones wait as `pending` for the
publishing worker or for Daw Mi to mark them posted. Index: `status WHERE status IN ('pending','failed')`.
A channel left `publishing` for 10 minutes is failed as `unknown_outcome` by the sweep, not sent again.

**`published_articles`** — What visitors read: a copy of the website version taken each time the post is
published, so an edit going back through review leaves the live article as it was. PK `post_id`, FK posts
`CASCADE`; columns as in `post_versions` (slug, title, excerpt, body, cover_image_id, seo_title, seo_description)
and `updated_at`. `slug` is unique (`409 slug_taken` when publishing a post whose slug another live article still
has). Public article reads return these copies, with the website publication's `published_at`, for posts not
`archived`, so archiving a post takes its article down; publishing with the website switched off removes it.

## Connections

**`connections`** — How the portal reaches a platform; one row per platform (`meta`, `linkedin`).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| platform | text | no | — | PK; `meta` or `linkedin` |
| status | text | no | — | `choosing_page` (Meta: signed in, several Pages) or `connected` |
| token | bytea | no | — | AES-256-GCM under `TOTP_ENCRYPTION_KEY`: Meta's long-lived user token while choosing, then the Page token; LinkedIn's member access token |
| account_id, account_name | text | yes | — | the Facebook Page, or the LinkedIn member id (`sub`) and name; required when `connected` |
| instagram_id, instagram_username | text | yes | — | the Instagram Business account linked to the Page |
| expires_at | timestamptz | yes | — | when the token's access ends (Meta: data access lapses after 90 days without signing in again; LinkedIn: about 60 days) |
| last_error | text | yes | — | `reconnect_required` once the platform refused the token; cleared by connecting again |
| connected_by, connected_at, updated_at | | | | `connected_by` FK users `SET NULL` |

Disconnecting deletes the row and so the token.

## Media

**`media`** — The media library: images for posts. Object keys derive from the id: the original, re-encoded,
at `originals/<id>.jpg` (private) and each web size at `public/<id>/<width>.jpg`, served from `MEDIA_PUBLIC_URL`.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| width, height | int | no | — | of the stored original, turned upright by its EXIF orientation |
| widths | int[] | no | — | web sizes stored, largest first: 1600, 800, 400, never wider than the image |
| byte_size | bigint | no | — | of the upload (≤ 20 MB) |
| alt | localized | yes | — | alt text, EN and MY, each ≤ 300 |
| credit | text | yes | — | ≤ 300 |
| uploaded_by | uuid | yes | — | FK users `SET NULL` |
| version, created_at, updated_at | | no | | |

Every stored file is re-encoded as JPEG, so no camera, time or location metadata survives. An item cannot be
deleted while any post version, whatever the post's status, or published article uses it (`409 in_use`). Index: `created_at`.

## Settings keys

Group `booking` is editable by `booking_admin`, group `site` by `site_admin` only.

| Key | Type | Default | Group | Requirement that makes it configurable |
|---|---|---|---|---|
| `timezone` | IANA name | `"Australia/Sydney"` | site | "There should be one clear production booking timezone." |
| `booking_mode` | `request_approval` \| `instant` | `"request_approval"` | booking | "Request & Approval is the provisional baseline. Instant confirmation remains possible later." |
| `public_booking_enabled` | boolean | `true` | booking | "Public Booking may be paused without deleting recurring availability" |
| `pending_hold_hours` | int 1–168 | `48` | booking | "The final hold/expiry rule remains pending." |
| `reminder_hours` | int 1–168 | `24` | booking | "Support at least one configurable reminder before confirmed appointments." |
| `min_notice_hours` | int 0–720 | `24` | booking | "configurable minimum booking notice and maximum advance-booking limits" |
| `max_advance_days` | int 1–365 | `60` | booking | same sentence |
| `slot_step_minutes` | int 5–120 | `30` | booking | "working hours … should be configurable without routine developer changes" |
| `cancellation_notice_hours` | int 0–168 | `48` | booking | "cancellation/rescheduling policy" (48 h confirmed by Daw Mi) |
| `late_cancellation_fee_percent` | int 0–100 | `50` | booking | "Late-cancellation rules or fees, if any" |
| `late_cancellation_first_waived` | boolean | `true` | booking | same |
| `no_show_fee_percent` | int 0–100 | `100` | booking | same |
| `meeting_link_mode` | `vetmimi_room` \| `manual_link` | `"vetmimi_room"` | booking | "The online appointment meeting-link process remains undecided." |
| `payment_methods` | array of `bank_transfer`, `card` | `["bank_transfer","card"]` | booking | "fee/payment information if applicable" in confirmations |
| `invoice_timing` | `after_session` | `"after_session"` | booking | same; Daw Mi invoices after the session |
| `retention_months` | int 6–120 | `24` | site | "Booking data should not be retained indefinitely" |
| `contact_email` | email | `"meenaerie@gmail.com"` | site | "Sender/reply-to details must use Daw Mi's approved professional contact information." |
| `response_time` | localized | `{"en":"Usually within 2 business days"}` | site | Contact page "response-time wording" |

Fees are wording for emails and the cancel screen; the API never charges. Per-service duration and buffers are
columns on `services` (seeded above), edited by the same role as these keys.

## Indexes

Beyond primary keys: `users` unique `email`, unique partial `is_practitioner`; `sessions` unique `token_hash`,
`user_id`, `expires_at`; `services` unique `slug`; `availability_rules_no_overlap` (GiST); `availability_overrides
(on_date)`; `availability_blocks` GiST `period`; `appointments` unique `reference`, unique `management_token_hash`,
`appointments_no_overlap` (GiST), `(status, starts_at)`, `(service_id, starts_at)`, `visitor_email`,
`hold_expires_at WHERE status = 'pending'`; `appointment_events(appointment_id, created_at)`; `video_rooms` unique
`appointment_id`, unique `join_token_hash`; `contact_enquiries` unique `reference`, `(status, created_at)`;
`communications(appointment_id, created_at)`, `scheduled_for WHERE status = 'queued'`, `created_at WHERE status =
'failed'`; `idempotency_keys(created_at)`; `posts(status, created_at)`; `post_versions_slug_key` unique `slug WHERE channel
= 'website'`; `post_publications(status) WHERE status IN ('pending','failed')`.

## What is deliberately not stored

- **Clinical data**: no diagnosis, medication, trauma or treatment history, session notes or outcomes;
  `visitor_note` is practical and capped at 500 characters, `admin_note` is for scheduling.
- **Card numbers or payment details**: Daw Mi invoices after the session. Online payment, if it comes, would add
  only a provider reference and a payment status kept apart from appointment status.
- **Recordings**: video media never touches the API; signaling messages are relayed, not stored.
- **Marketing data**: no mailing lists, marketing consent or tracking; VetMiMi never sends marketing email.
- **Usable tokens or secrets**: tokens are stored as hashes (link tokens also as seeds that need
  `SIGNING_SECRET`); TOTP secrets are encrypted. Original True Story sources: only a reference to where they are.
- **External calendar data and service-specific availability**: not in the launch scope.
