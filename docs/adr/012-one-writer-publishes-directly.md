# 012 — One writer publishes directly, without a review step

**Status:** Accepted · 2026-11-07. Amends ADR-009.

## Context

ADR-009 gave posts a review step: a content editor submits, and only a site
administrator approves, then schedules or publishes. In practice Daw Mi is the
only person who writes posts, so she submitted and approved her own work
before every post: two clicks that checked nothing a person had not just seen.

## Decision

- A site administrator schedules or publishes straight from an idea or draft.
  The platform checks that approval used to run (each enabled channel's
  rules, images in the media library, a True Story's consent) run at that
  moment instead, and `approved_at`/`approved_by` record who cleared it.
- Unscheduling, or editing a scheduled or published post, returns it to
  `draft`; it is checked again when it is next scheduled or published.
- Submit, request changes and approve are removed from the API and the admin.
  The `content_editor` role still writes drafts; only `site_admin` publishes.

## Consequences

- One step from a finished draft to a live post, with the same checks.
- `in_review`, `approved` and `review_note` stay in the schema for posts
  saved before this change; nothing new reaches them.
- If a second writer joins, a review step can come back as its own decision.
