# 006 — Communications as durable records, delivered by asynq workers

**Status:** Accepted · 2026-10-04

## Context

The requirements separate appointment status from communication status, want a
communication history per appointment (Queued, Sent, Failed), one configurable
reminder, and say an email failure must never invalidate a booking. Emails are
needed in English and Burmese.

## Options considered

- **Send inline in the request.** Simplest; a slow or failing mail provider
  makes the booking request fail or hang.
- **Fire-and-forget goroutine.** Loses the message on crash; no history.
- **Durable row + background delivery.** The row is the history the
  requirements ask for anyway. Chosen.

## Decision

- Every message is first a row in `communications` (`appointment_id`,
  `kind`, `channel = 'email'`, `recipient`, `locale`, `status`,
  `scheduled_for`, `sent_at`, `provider_message_id`, `error`), written in the
  same transaction as the change that caused it.
- After commit, the API enqueues an asynq task carrying only the row id. The
  worker loads the row, renders the template, sends through Resend, and
  updates `status`, `sent_at` or `error`. Retries with backoff up to 5 times,
  then `failed`, which surfaces in the admin's "Attention required".
- Reminders are asynq tasks scheduled at `starts_at − reminder_hours`
  (settings row, default 24). The task checks the appointment is still
  `confirmed` at run time and skips otherwise, so cancellations never send a
  reminder.
- Hold expiry (ADR-004) is the same mechanism: a task scheduled at
  `hold_expires_at` that re-checks status before acting.
- Templates live in `internal/comms/templates/<kind>.<locale>.tmpl`, one per
  message kind and locale, with the practice's approved wording; subject and
  body are Go `text/template` + `html/template`. The visitor's locale is the
  one they booked in.
- Admin actions "Resend" and "Mark as communicated" create new rows rather
  than editing old ones, so the history stays truthful.

## Consequences

- Booking requests return in milliseconds regardless of the mail provider.
- A crash between commit and enqueue leaves a `queued` row with no task; a
  sweeper task every 5 minutes enqueues any `queued` row older than a minute.
- Redis is required in production and development. Losing Redis loses only
  pending tasks, which the sweeper and the reminder scheduler rebuild from
  PostgreSQL on restart.
