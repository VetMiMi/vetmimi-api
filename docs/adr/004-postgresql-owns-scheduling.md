# 004 — PostgreSQL owns scheduling correctness

**Status:** Accepted · 2026-10-04

## Context

The Appointment Booking Requirements demand: no double booking, slots
revalidated at submission, pending requests holding their slot, rescheduling
that secures the new time before releasing the old, and correct behaviour
across daylight-saving changes. Two visitors can submit the same slot in the
same second; Daw Mi can block a day while a request is being confirmed.

## Options considered

- **Check-then-insert in Go.** Simple to read, wrong under concurrency unless
  wrapped in serialisable transactions with retries.
- **Redis locks.** Another moving part, and a lock that expires at the wrong
  moment lets a double booking through.
- **A PostgreSQL exclusion constraint on a time range.** The database rejects
  the second overlapping row no matter how many processes race. Chosen; it is
  the same principle that made AU-Van's seat claims correct.

## Decision

- `appointments.busy_range tstzrange` covers the session **plus its before and
  after buffers**, computed in Go from the service's settings at write time.
- Constraint, created with `btree_gist`:

  ```sql
  ALTER TABLE appointments ADD CONSTRAINT appointments_no_overlap
    EXCLUDE USING gist (practitioner_id WITH =, busy_range WITH &&)
    WHERE (status IN ('pending', 'confirmed'));
  ```

  Declined, cancelled, completed and no-show rows fall out of the constraint
  automatically, so a cancelled slot reopens with a status change.
- A pending request **is** the hold: status `pending`, `hold_expires_at`
  set from the `pending_hold_hours` setting (default 48). An asynq job expires
  it to `expired` and tells the visitor; Daw Mi confirming before then clears
  the expiry.
- Public availability is computed in Go: recurring weekly rules + one-off
  openings + date overrides − blocks − busy ranges − buffers, inside the
  minimum-notice and maximum-advance window, all in the practice time zone
  (`settings.timezone`, `time.LoadLocation`). It is a read model; the
  constraint is the truth. A visitor who submits a slot that just vanished gets
  `409 slot_unavailable` with fresh alternatives.
- Rescheduling is one transaction: `UPDATE appointments SET busy_range = $new`
  — the constraint checks the new range before the row is committed, so the
  old time is never released without the new one being secured. The previous
  range is written to `appointment_events`.
- Creation takes an `Idempotency-Key`; the key and the response are stored for
  24 hours so a retried submit returns the same appointment.
- Everything is `timestamptz`; nothing is stored as local wall-clock time.

## Consequences

- Correctness does not depend on the Go code being race-free; the constraint
  is the guard, and a test proves it by deleting it.
- Changing a service's buffers changes future `busy_range` computation only;
  existing rows keep the range they were booked with.
- Daylight-saving transitions need explicit tests on the slot generator
  (the first Sunday of April and October in Sydney).
- The exclusion index must be the first migration after `appointments`, and
  the implementer must show the concurrency test that fails without it.
