// Package booking holds services, availability rules, slot generation and
// appointments. PostgreSQL is the authority for scheduling (ADR-004): the
// constraints in migrations/ guard every rule, and this package turns their
// refusals into friendlier errors.
package booking
