# Data model

The launch tables in migration order. Decisions are in `docs/adr/`; a migration updates this file in the same PR.

## Conventions

- Primary keys are `uuid DEFAULT gen_random_uuid()` unless stated; append-only logs use `bigint` identity.
- Every instant is `timestamptz`. The only wall-clock values are the weekday and times in `availability_rules`.
- Status and kind columns are `text` with `CHECK (col IN (…))`, not PostgreSQL enums, so adding a value is one
  migration line. Allowed transitions live in Go (`internal/booking/status.go`, `internal/content/status.go`).
- `localized` is a domain over `jsonb`: an object whose only keys are `en` and `my`. **en req.** adds
  `CHECK (col ? 'en')`. Public reads fall back to `en`. Rich text is `localized` holding a TipTap document per locale.
- `permission` means `text CHECK IN ('not_required','pending','approved','refused')`. `…_by` columns are `uuid`
  FK users `ON DELETE SET NULL`. Emails are stored lower-case; slugs match `^[a-z0-9]+(-[a-z0-9]+)*$`.
- Admin-edited rows carry `version int DEFAULT 1`; writes send the version they read; a mismatch is
  `409 stale_version`. `created_at`/`updated_at` (`now()`) are on every table and listed only where notable.

## settings

Business rules Daw Mi may change without a deploy, one row per key (see "Settings keys"), seeded with defaults.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| key | text | no | — | PK; unknown keys are refused by the API |
| value | jsonb | no | — | typed and range-checked per key in `platform/settings` |
| updated_by, updated_at | uuid, timestamptz | yes, no | —, now() | |

## Accounts

**`users`** — Administrators. Daw Mi is the one practitioner.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| email, display_name | text | no | — | email unique |
| password_hash | text | no | — | argon2id PHC string |
| roles | text[] | no | — | subset of `content_editor`, `booking_admin`, `site_admin`; at least one |
| is_practitioner | boolean | no | false | the person appointments are booked with |
| totp_secret_enc, totp_last_step | bytea, bigint | no, yes | — | AES-256-GCM ciphertext; last accepted step (replay guard), set to the enrolment code's step by create-user |
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

Indexes: unique `token_hash`; `user_id`; `expires_at`.

## services

What can be booked or enquired about, with its scheduling facts. Editorial copy lives in `service_pages`.

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
as `conflicts`; nothing is cancelled automatically. Block writes and appointment creation share an advisory lock.

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

Kinds: `request_received`, `booking_confirmed`, `request_declined`, `rescheduled`, `cancelled`, `reminder`,
`request_expired` (visitor); `practitioner_new_request`, `practitioner_new_booking`,
`practitioner_client_cancelled`, `practitioner_reschedule_requested`, `practitioner_new_enquiry` (practitioner).
Constraints: `CHECK (num_nonnulls(appointment_id, contact_enquiry_id) = 1)`, `CHECK (status <> 'sent' OR sent_at
IS NOT NULL)`, `CHECK (channel = 'manual' OR recipient IS NOT NULL)`. Transitions: `queued → sent | failed |
cancelled`, all final; Resend and Mark as communicated insert new rows. Indexes: `(appointment_id, created_at)`,
`scheduled_for WHERE status = 'queued'`, `created_at WHERE status = 'failed'`.

**`idempotency_keys`** — Stored responses for retried creates (ADR-004), kept 24 hours.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| scope | text | no | — | `public_appointment`, `admin_appointment`, `contact_enquiry` |
| key | text | no | — | as sent in `Idempotency-Key`, ≤ 128 |
| request_hash | bytea | no | — | SHA-256 of the request body |
| resource_id, response_status, response_body | uuid, smallint, jsonb | yes | — | the row created and the response sent |
| created_at | timestamptz | no | now() | |

PK `(scope, key)`; index `created_at`. Inserted in the creating transaction, so a failed create leaves no key.

## media

The central media library (ADR-008).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| name | text | no | — | asset name |
| asset_type | text | no | — | `portrait`, `artwork`, `workshop_photo`, `event_photo`, `art_of_wellness`, `story_image`, `logo`, `social_image`, `document` |
| file_name, mime_type | text | no | — | as uploaded; type sniffed: JPEG, PNG, WebP or PDF |
| byte_size, width, height | bigint, int, int | no, yes, yes | — | ≤ 20 MiB |
| sha256, original_key | bytea, text | no | — | duplicate warning on upload; bucket key under private `originals/` |
| derivatives, processing_status | jsonb, text | no | `'[]'`, `'processing'` | `[{"width":1600,"key":"public/…webp","bytes":…}]` for 1600, 800, 400; `processing`, `ready`, `failed` |
| alt, caption | localized | yes | — | English alt required before public use |
| credit, creator, copyright_holder, related_note | text | yes | — | creator = photographer or artist; related_note internal context |
| credit_required, year | boolean, smallint | no, yes | false, — | |
| asset_status | text | no | `'needs_information'` | `approved`, `permission_pending`, `do_not_use`, `needs_information`, `needs_better_quality` |
| website_permission, facebook_permission | permission | no | `'pending'` | independent of each other |
| organisation_approval, people_consent | permission | no | `'not_required'` | |
| people_in_image | boolean | no | false | |
| archived_at, created_by, updated_by, version | | yes | — | archived media is never served |

Constraint: `CHECK (NOT people_in_image OR people_consent <> 'not_required')`. Usable on the website = `ready`,
not archived, `asset_status = 'approved'`, `website_permission IN ('approved','not_required')`, and consent
`approved` when people appear. Indexes: `asset_type`, `asset_status`, `sha256`, `lower(name)`.

## Shared content columns

`stories`, `service_pages`, `portfolio_items` and `pages` each carry these (ADR-008).

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| slug | text | no | — | unique per table |
| publication_status | text | no | `'draft'` | `draft`, `review`, `scheduled`, `published`, `unpublished`, `archived` |
| approval_status | text | no | `'not_reviewed'` | `not_reviewed`, `needs_review`, `changes_requested`, `approved` |
| publish_at, published_at, last_published_at | timestamptz | yes | — | set while `scheduled`; first publication (the date shown); latest |
| published_by, published_version | uuid, int | yes | — | `published_version` = row version that is live |
| published_snapshot | jsonb | yes | — | public projection built at publish; the only thing public routes read |
| approved_at, approved_by, review_note | | yes | — | review_note = internal note from Request changes |
| seo_title, seo_description, social_title, social_description | localized | yes | — | default from title and excerpt; social image via `content_media` |
| noindex, featured | boolean | no | false | `featured` is true while the record holds any `featured_slots` row |
| sort_order, maintenance_status | int, text | no | 0, `'current'` | `current`, `review_due`, `needs_update`, `verification_pending` |
| review_due_on, last_verified_at, owner_user_id, created_by, updated_by, version | | | | owner = content owner |

Constraints: `CHECK (publication_status <> 'scheduled' OR publish_at IS NOT NULL)`, `CHECK (publication_status <>
'published' OR published_snapshot IS NOT NULL)`. Indexes: unique `slug`; `(publication_status, published_at DESC)`.

Publication: `draft|unpublished → review` (submit-for-review); `review → draft` (request-changes);
`draft|review|unpublished → scheduled` (schedule); `draft|review|unpublished|scheduled|published → published`
(publish or the scheduled job; republishing makes saved changes live); `scheduled → draft` and `published →
unpublished` (unpublish); any → `archived` (archive); `archived → draft` (restore a version). Schedule and publish
need `approval_status = approved`.

Approval: `not_reviewed|changes_requested → needs_review` (submit); `not_reviewed|needs_review|changes_requested →
approved` (approve); `needs_review → changes_requested`; `approved → needs_review` automatically on a significant
edit (title, body, excerpt, images, category, consent or permission fields, page sections). Editing a published
record leaves the live snapshot untouched until approved and published again; "unpublished changes" means
`version > published_version`.

## Content records

**`stories`** — Stories & Insights.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| kind | text | no | `'insight'` | `story` (a person's lived experience) or `insight` (Daw Mi's writing) |
| category | text | no | — | `true_stories`, `reflections`, `art_and_wellbeing`, `art_psychotherapy`, `art_of_wellness_updates` |
| title, excerpt | localized | no | — | **en req.** |
| subtitle, body | localized | yes | — | body is rich text; the excerpt is shown when it is empty |
| author_name, source_reference | text | no, yes | — | author or storyteller as displayed; internal: where the original is kept |
| transcription_status | text | no | `'not_applicable'` | `not_applicable`, `pending`, `verified` |
| storyteller_approval, real_name_permission, image_permission, artwork_permission, website_permission, facebook_permission | permission | no | `'not_required'` | True Story safeguards |
| privacy_review, safeguard_notes | text | no, yes | `'not_applicable'`, — | `not_applicable`, `pending`, `complete`; internal notes |

Plus the shared content columns. Choosing `true_stories` sets the safeguards to `pending` and makes them publish
requirements. Facebook reuse status is read from `facebook_posts`. Images: `content_media` `featured`, `gallery`, `social`.

**`service_pages`** — The public page for a service; at most one per service.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| service_id | uuid | no | — | FK services `RESTRICT`; unique. Name, duration, formats, fee and action come from it |
| tone | text | no | `'rose'` | `rose`, `blue`, `gold` |
| card_label, teaser | localized | yes | — | home card eyebrow ("ONE-TO-ONE") and text |
| audience, summary, cta_label | localized | no | — | **en req.**; the CTA target follows `services.booking_action` |
| intro, fit, reassurance, steps_title, closing_title, closing_text, compare_quote | localized | yes | — | |
| highlights | jsonb | no | `'[]'` | array of localized strings |
| steps, practical | jsonb | no | `'[]'` | `[{"title": localized, "text": localized}]` |
| questions | jsonb | no | `'[]'` | `[{"question": localized, "answer": localized}]` |

Plus the shared content columns; `slug` equals `services.slug` (checked in Go).

**`portfolio_items`** — Artwork, workshops and programs, exhibitions and events, projects and collaborations.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| category | text | no | — | `artwork`, `workshops_programs`, `exhibitions_events`, `projects_collaborations` |
| title | localized | no | — | **en req.**; artwork titles are usually English only |
| year | smallint | yes | — | only when confirmed |
| date_label, summary, body, role, organisation, medium, context | localized | yes | — | "Since 2023"; short text; rich text; Daw Mi's exact role; organisation or venue; medium; context or reflection, e.g. "From her CECAT essay, 2024" |
| artist, dimensions | text | yes | — | |
| external_links | jsonb | no | `'[]'` | `[{"url": text, "label": localized}]`; site-relative paths allowed |
| evidence_note | text | yes | — | internal: source for institutional claims |

Plus the shared content columns. Images: `content_media` `featured`, `gallery`, `social`.

**`pages` and `page_sections`** — Structured pages: `home`, `about`, `contact`, `art-of-wellness`, the index pages
`services`, `stories`, `portfolio`, and `privacy`, `disclaimer`, `booking-policy`. `pages` has `title localized`
(**en req.**) plus the shared content columns; a page is reviewed, versioned and published with its sections.

| page_sections column | Type | Null | Default | Notes |
|---|---|---|---|---|
| page_id | uuid | no | — | FK pages `CASCADE` |
| key | text | no | — | stable per page: `hero`, `why`, `team`… |
| kind | text | no | — | `hero`, `text`, `quote`, `cards`, `team`, `faq`, `gallery`, `quotes`, `cta`, `contact_details`, `legal`, `featured` |
| position | int | no | — | order on the page |
| content | jsonb | no | `'{}'` | per-kind shape checked in Go; string leaves are localized; images `{"media": "<uuid>"}`; `featured` names an area |
| hidden | boolean | no | false | |

Constraints: unique `(page_id, key)`; unique `(page_id, position) DEFERRABLE INITIALLY DEFERRED`. The contact
page's email and response time come from `settings` at publish, so the address lives in one place.

Import (ADR-008): each top-level object of `home.json`, `about.json`, `contact.json`, `artOfWellness.json`,
`legal.json`, and the `index`/`header`/`end` objects of `stories.json`, `services.json` and `portfolio.json`,
becomes a section. `items.<slug>` objects become `stories`, `service_pages` and `portfolio_items` rows; portfolio
`highlights` and `artworks` become portfolio items placed by `featured_slots`. UI chrome (buttons, filters,
breadcrumbs, form labels, the `book.json` steps) stays in `messages/`.

## Content links and history

**`content_media`** — Every link from content to media, and the guard against deleting media in use.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| content_type | text | no | — | `story`, `service_page`, `portfolio_item`, `page`, `facebook_post` |
| content_id | uuid | no | — | polymorphic, no FK |
| section_id | uuid | yes | — | FK page_sections `CASCADE` |
| media_id | uuid | no | — | FK media `RESTRICT` → `409 in_use` |
| role | text | no | — | `featured`, `gallery`, `social`, `section`, `attachment` |
| position | int | no | 0 | |

Unique `(content_type, content_id, role, position, section_id) NULLS NOT DISTINCT`; index `media_id`. Rewritten
on every save. Content is archived rather than deleted; a hard delete removes its links in the same transaction.

**`content_versions`** — A snapshot per change, for history and restore. `id bigint` identity.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| content_type | text | no | — | `story`, `service_page`, `portfolio_item`, `page` |
| content_id | uuid | no | — | |
| version, action | int, text | no | — | row version after the change; `import`, `save`, `submit`, `approve`, `request_changes`, `schedule`, `publish`, `unpublish`, `archive`, `restore` |
| snapshot | jsonb | no | — | full row incl. internal fields; pages include sections; media links |
| created_by, created_at | | | | null `created_by` = system |

Unique `(content_type, content_id, version)`. Restore copies a snapshot's editable fields forward as a new version.

**`content_relations`** — Deliberate links: story ↔ portfolio item, service → story, Art of Wellness page → projects.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| from_type, to_type | text | no | — | `story`, `service_page`, `portfolio_item`, `page` |
| from_id, to_id | uuid | no | — | |
| position | int | no | 0 | order shown on the source |
| created_by, created_at | | | | |

Unique `(from_type, from_id, to_type, to_id)`; `CHECK (from_id <> to_id)`; index `(to_type, to_id)`. Public
reads return only related records that are published.

**`featured_slots`** — Ordered featured records per area, chosen from existing records.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| area | text | no | — | see below |
| position | int | no | — | 0-based |
| content_type, content_id | text, uuid | no | — | type allowed for the area |
| created_by, created_at | | | | |

PK `(area, position)`; unique `(area, content_type, content_id)`. Areas: `home_services` (service_page),
`home_gallery`, `home_portfolio`, `portfolio_highlights`, `portfolio_artworks` (portfolio_item), `home_stories`,
`stories_featured` (story), `art_of_wellness_projects` (portfolio_item, story). Saving an area replaces its rows
in one transaction. Public reads skip records that are not published, so no card breaks; the admin list flags them.

**`slug_redirects`** — Old slugs of published records, so older links (including Facebook posts) keep working.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| content_type, old_slug | text | no | — | PK together |
| content_id | uuid | no | — | |

A public read by an old slug returns the record with its current `slug`; the site redirects permanently.

## facebook_posts

Facebook channel tracking, independent of website status; publishing is manual at launch.

| Column | Type | Null | Default | Notes |
|---|---|---|---|---|
| source_type, source_id | text, uuid | yes | — | `story`, `service_page`, `portfolio_item`, `page`; both null for Facebook-only posts |
| working_title, copy, locale | text | no | —, `''`, `'en'` | ≤ 200; adapted post text ≤ 5,000 |
| link_url, post_url | text | yes | — | website destination; `https://www.facebook.com/…` |
| status | text | no | `'draft'` | `not_planned`, `draft`, `needs_review`, `permission_pending`, `approved`, `scheduled`, `published`, `withdrawn`, `failed` |
| facebook_permission, ai_assisted | permission, boolean | no | `'pending'`, false | AI-drafted copy is always reviewed |
| planned_publish_at, published_at, last_checked_at | timestamptz | yes | — | last checked = publication verified |
| failure_note, correction_note, approved_by, approved_at, published_by, owner_user_id, created_by, updated_by, version | | | | notes are internal |

Constraints: `CHECK (num_nulls(source_type, source_id) IN (0, 2))`; `CHECK (status <> 'published' OR (post_url IS
NOT NULL AND published_at IS NOT NULL))`; `CHECK (status NOT IN ('approved','scheduled','published') OR
facebook_permission IN ('approved','not_required'))`. Indexes: `(status, planned_publish_at)`,
`(source_type, source_id)`. Transitions: `not_planned ⇄ draft → needs_review → draft | permission_pending |
approved`; `permission_pending → approved → scheduled | published | failed`; `scheduled → approved | published |
failed`; `failed → approved | published`; `published → withdrawn`. A post linking to website content can be
`published` only once that content is (`422 website_not_published`).

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
'failed'`; `idempotency_keys(created_at)`; `media` `asset_type`, `asset_status`, `sha256`, `lower(name)`; every
content table unique `slug` and `(publication_status, published_at DESC)`; `service_pages` unique `service_id`;
`page_sections` unique `(page_id, key)`, `(page_id, position)`; `content_media(media_id)` and its unique tuple;
`content_versions` unique `(content_type, content_id, version)`; `content_relations` unique tuple, `(to_type,
to_id)`; `featured_slots` unique `(area, content_type, content_id)`; `facebook_posts(status, planned_publish_at)`,
`(source_type, source_id)`.

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
