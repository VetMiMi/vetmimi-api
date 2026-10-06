# Architecture decision records

One file per decision, numbered in merge order. The orchestrator allocates the
next number so concurrent tracks never claim the same one. A record is never
rewritten once accepted; a later record supersedes it and says so.

Template:

```markdown
# NNN — Title

**Status:** Accepted · YYYY-MM-DD

## Context
What forced the decision, including constraints from the requirement
documents, the budget, the hardware and the owner's goals.

## Options considered
Two or more, each with the one reason it was attractive and the one reason
it lost.

## Decision
What we do, in enough detail that an implementer does not have to guess.

## Consequences
What becomes easier, what becomes harder, what we must now watch.
```

| # | Title |
|---|---|
| [001](001-go-api-in-its-own-repository.md) | Go API in its own repository |
| [002](002-custom-admin-in-the-next-app.md) | Custom admin inside the Next.js app, Next as the browser's only origin |
| [003](003-openapi-first-contract.md) | OpenAPI-first contract shared by generation |
| [004](004-postgresql-owns-scheduling.md) | PostgreSQL owns scheduling correctness |
| [005](005-cheap-live-host-and-fargate-target.md) | One cheap live host; ECS Fargate kept as a validated Terraform target |
| [006](006-communications-as-durable-records.md) | Communications as durable records, delivered by asynq workers |
| [007](007-one-to-one-webrtc-with-go-signaling.md) | One-to-one WebRTC with Go WebSocket signaling |
| [008](008-content-in-postgres-served-through-the-api.md) | Content in PostgreSQL, served through the API, revalidated by tag |
| [009](009-publishing-portal.md) | A publishing portal for posts, replacing the website CMS |
