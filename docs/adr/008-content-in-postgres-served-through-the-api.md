# 008 — Content in PostgreSQL, served through the API, revalidated by tag

**Status:** Superseded by [009](009-publishing-portal.md) · 2026-10-06

## Context

Site text currently lives in `messages/<locale>/*.json` and structured data in
`lib/data.ts`, edited by developers. The Content Management Requirements want
Daw Mi to draft, review, approve, schedule, publish, unpublish and archive
Stories & Insights, Services, Portfolio items, the Art of Wellness page and
page sections, with a media library, consent fields for True Stories,
featured selection, SEO fields, and history — all without code changes, and
with the site remaining fast and statically served where possible.

## Options considered

- **Keep JSON in git, add an editor that commits.** Keeps the current speed;
  commits as a publishing model confuse the roles the requirements define and
  cannot schedule or hold approval state.
- **Headless CMS service.** Rejected in ADR-002 for the same reasons as
  Payload.
- **Content tables in PostgreSQL behind the API, the site fetching with cache
  tags and revalidating on publish.** One source of truth, fast reads, no
  rebuilds. Chosen.

## Decision

- Each content type is a table (`stories`, `service_pages`, `portfolio_items`,
  `pages`, `media`) with a shared shape: `slug`, localised fields as
  `jsonb {"en": …, "my": …}`, `publication_status`
  (`draft`, `review`, `scheduled`, `published`, `unpublished`, `archived`),
  `approval_status` (`not_reviewed`, `needs_review`, `changes_requested`,
  `approved`), `publish_at`, SEO fields, `featured`, `sort_order`, and
  `created_by`/`updated_by`/timestamps. Internal fields (consent, permissions,
  evidence) are columns the public endpoints never select.
- Rich text is stored as **portable JSON** (TipTap document), rendered by the
  site; never raw HTML from the editor.
- Every save writes a `content_versions` row (type, id, full JSON snapshot,
  author, time); restore is "copy a version forward".
- A significant edit after approval resets `approval_status` to
  `needs_review`; "significant" is any change to title, body, images, or
  consent fields.
- Public read endpoints (`GET /public/stories?locale=…` etc.) return only
  published records and only public fields. The site fetches them in server
  components with `next: { tags: ['stories', 'story:<slug>'] }`. On
  publish/unpublish/schedule-fire the API calls the site's
  `POST /api/revalidate` with the tags and a shared secret.
- Scheduled publishing is an asynq task at `publish_at` (ADR-006 mechanism)
  that re-checks approval before flipping status.
- Media: uploads go through the API (`multipart`, 20 MB cap), originals kept,
  web derivatives (1600, 800, 400 wide WebP) generated on upload, stored in an
  S3-compatible bucket, served from its public URL. The `media` row carries
  alt text per locale, caption, credit, copyright holder, website/Facebook
  permission states and the people-consent flag. Deleting media in use is
  refused; archive instead.
- Migration: a one-off `cmd/import-content` reads the current JSON and
  `lib/data.ts` output and writes published rows, so launch content is
  identical to today's site.

## Consequences

- The site's pages change from reading JSON at build to fetching at request
  with long-lived cached responses; first byte stays fast, and a publish is
  visible within a second.
- `messages/*.json` shrinks to UI chrome (navigation, buttons, form labels);
  editorial text moves to the database.
- Facebook publishing starts manual-first: channel status, an "adapt for
  Facebook" draft (AI-assisted, never auto-published) and a post-link field,
  per the Website & Facebook Publishing Workflow. The Graph API is a later
  ADR.
