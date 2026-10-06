# 009 — A publishing portal for posts, replacing the website CMS

**Status:** Accepted · 2026-10-06 · Supersedes [008](008-content-in-postgres-served-through-the-api.md)

## Context

ADR-008 planned a website CMS: every page, section, story, service page and
portfolio item in PostgreSQL, with featured areas, version history, a rich
media-permission model and manual Facebook tracking. None of it was built.
What Daw Mi actually needs is to write a piece once and share it: as an
article on the website and as posts on her Facebook Page, her Instagram
Business account (linked to the Page) and her LinkedIn profile. The static
pages (Home, About, Services, Portfolio, Art of Wellness) change rarely and
read well from the site's code.

## Decision

- Content management is a **publishing portal**. A **post** has a working
  title, a kind (`insight`, `true_story`, `announcement`) and one **version per
  channel** (`website`, `facebook`, `instagram`, `linkedin`), each enabled or
  not. The website version holds the article: slug, English and Burmese
  title, excerpt and Markdown body, cover image and SEO fields. Social versions
  hold the text (or caption), an optional link and images.
- One workflow: idea → draft → in review → approved → scheduled → publishing
  → published, or archived. Content editors write and submit; only a site
  administrator approves, schedules, publishes and archives. Approval checks
  every enabled version against its platform's rules (Instagram needs an
  image, at most 2,200 characters and 30 hashtags; LinkedIn at most 3,000
  characters) and a True Story's consent. Editing an approved post sends it
  back to review.
- Each enabled channel gets a **publication** with its own status (`pending`,
  `publishing`, `published`, `failed`, `manual`). The website publishes
  directly; social channels go through connectors, and when a connection is
  missing or fails Daw Mi posts it herself (**copy & open**) and marks it
  posted. An AI helper may suggest channel versions; she always edits and
  approves them.
- The site reads `GET /public/articles` and `/public/articles/{slug}`:
  published website versions only, in the visitor's locale with English as
  the fallback. Bodies are stored as written; the site renders Markdown
  safely.
- The ADR-008 contract (stories, service pages, portfolio, pages, sections,
  featured areas, Facebook tracking and the old media library) is removed
  from `openapi.yaml`.

## Consequences

- One editing flow instead of five content types; the schema is three tables
  (`posts`, `post_versions`, `post_publications`).
- The static pages stay in `vetmimi-next`; changing them is a code change.
- Scheduled publishing, site revalidation, the media library and the Meta
  and LinkedIn connectors arrive as separate issues on top of this model.
- A post cannot be edited once publishing starts. Correcting a live article
  needs a later decision (a published snapshot, or edit-and-republish).
