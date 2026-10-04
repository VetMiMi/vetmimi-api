# 005 — One cheap live host; ECS Fargate kept as a validated Terraform target

**Status:** Accepted · 2026-10-04

## Context

Daw Mi's practice is small and the running cost must stay near zero. The owner
also wants the deployment to demonstrate a production-grade AWS topology
(ECS Fargate, ECR, RDS, ALB, CloudWatch, health-gated releases, rollback), as
his résumé describes. The two goals conflict on price: Fargate + ALB + RDS is
roughly A$60–80 a month.

## Options considered

- **Run Fargate for real.** Honest to the résumé; too expensive for the
  practice unless someone sponsors it.
- **Only a cheap host, no AWS.** Cheapest; nothing to show about cloud
  operations.
- **Both: live on a cheap host, Fargate as Terraform that is validated and can
  be applied for a demo and torn down.** The AU-Van approach. Chosen.

## Decision

- **Live:** one small VPS (2 vCPU / 2 GB, ~A$10/month; provider the owner's
  choice) running Docker Compose from `deploy/compose/`: `api`, `worker`
  (same image, `--mode worker`), `postgres:17`, `redis:7`, `caddy` (TLS,
  reverse proxy), `coturn` (ADR-007). Nightly `pg_dump` to an S3-compatible
  bucket; restores rehearsed in the runbook.
- **Deploy:** GitHub Actions on `main`: build once, push the image to GHCR
  tagged with the commit SHA, SSH to the host, `docker compose pull && up -d`,
  then poll `/healthz` for 60 seconds; on failure roll back to the previous
  tag automatically and fail the workflow. Secrets live in GitHub Environments
  and the host's `.env`, never in the repository.
- **Target:** `deploy/terraform/` describes ECR, an ECS Fargate service across
  two availability zones behind an ALB, RDS PostgreSQL, ElastiCache or a
  Redis sidecar, CloudWatch logs and alarms, with a budget alarm. CI runs
  `terraform fmt -check` and `terraform validate`. Agents never run `apply` or
  `destroy`; the owner applies it for a demo and tears it down.
- The site stays on Vercel. The API is reachable to Next only by its hostname
  plus the service key (ADR-002).

## Consequences

- Running cost stays near A$10/month plus domains; the AWS path is proven on
  demand, not paid for continuously.
- One host means planned downtime during image swaps of a few seconds; the
  compose file uses a health check so Caddy only routes to a healthy
  container.
- Backups and the restore runbook are a launch requirement, not a nice-to-have,
  because there is no managed database behind the live host.
