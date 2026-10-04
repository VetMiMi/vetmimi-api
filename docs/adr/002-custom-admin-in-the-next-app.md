# 002 — Custom admin inside the Next.js app, Next as the browser's only origin

**Status:** Accepted · 2026-10-04

## Context

The Content Management Requirements say not to assume a custom admin before
evaluating the content workflow, and the Booking & Admin UX spec describes a
booking administration area (Dashboard, Appointments, Availability, Settings)
that needs authentication and role separation from content editing. Both are
used by the same person, Daw Mi, from a phone as often as a laptop.

The owner wants the admin to feel as finished as the public pages, which
already carry a design system (`lib/tokens.ts`, Fraunces/Manrope/Caveat,
the Figma Make specification).

## Options considered

- **Payload CMS embedded in Next.** Drafts, versions, scheduled publishing,
  media and localisation out of the box. Lost because it brings a second
  admin shell with its own authentication and look, so booking and content
  would be two products, and because the owner wants the content workflow to
  be his own work.
- **Separate admin SPA (Vite) talking to the API directly.** Clean split.
  Lost because it duplicates the design system, needs CORS and cross-site
  cookies between Vercel preview domains and the API, and adds a third
  deployable.
- **Admin routes inside `vetmimi-next`, with the Next server as the only
  thing the browser talks to.** Reuses the design system and deployment;
  cookies stay first-party on every Vercel preview. Chosen.

## Decision

- Admin lives in `vetmimi-next` under `app/admin/`, outside `app/[locale]/`
  (English only; `proxy.ts` excludes `/admin` from locale routing). It reuses
  the public tokens and fonts with the calm "2/5 intensity" treatment the
  Figma spec assigns to Services and Booking.
- **Next is the browser's only origin** for HTTP. Server components and route
  handlers under `app/admin/` and the public booking/contact flows call the Go
  API server-side with `fetch`, sending `Authorization: Bearer <session>` for
  admin requests and a shared `X-Service-Key` header for public ones. The API
  origin is never exposed to the browser and needs no CORS.
- Sign-in: a route handler posts credentials (+ TOTP) to `POST /auth/sessions`
  on the API, which returns an opaque session token; Next stores it in an
  `httpOnly`, `Secure`, `SameSite=Lax` cookie scoped to the Next domain. Each
  admin request forwards it; the API validates and returns the role. Sessions
  expire after 12 hours idle; TOTP is required for Daw Mi's account.
- Roles come from the requirement documents: `content_editor`,
  `booking_admin`, `site_admin`. One person may hold several. Booking data is
  invisible to `content_editor`.
- The one exception to "Next is the only origin": the video session WebSocket
  (ADR-007) connects from the browser straight to the API with a short-lived
  room token that Next obtained server-side.

## Consequences

- One design system, one deployment for the UI, previews that just work.
- The Next server becomes a thin backend-for-frontend; its route handlers must
  stay thin (validate, forward, map errors) so logic does not drift into two
  places.
- The generated TypeScript client (ADR-003) is used only on the server side of
  Next; the browser sees Next's own routes.
- A `/admin` shell in `vetmimi-next` (layout, navigation, sign-in, session
  refresh, empty states, error states) is a prerequisite issue for every other
  admin feature.
