# 010 — The live host is an EC2 instance in Sydney, and it serves the website too

**Status:** Accepted · 2026-10-08, amended 2026-10-11 (images in ECR,
CloudWatch alarms). Updates ADR-005's live host and deploy sections; its
Fargate target stands.

## Context

ADR-005 chose "one small VPS (2 vCPU / 2 GB, ~A$10/month; provider the
owner's choice)" for the live stack and left the site on Vercel. Two things
have since been decided by the owner:

- The owner's AWS account holds promotional credits, which pay for a small
  instance for now, and the AU-Van project already runs the same shape there
  (its ADR-014 and ADR-015): Compose on one `t4g.small`, Caddy, deploys over a
  forced-command SSH key.
- With a host already paid for, the website can run beside the API instead of
  on Vercel, so the site, the API and the database share one origin network
  and one bill.

## Options considered

- **A VPS from another provider, site on Vercel (ADR-005 as written).**
  Cheapest in cash, but paid by card rather than credits, with no Terraform
  provider the owner already uses, and the site's server calls to the API
  cross the internet.
- **ECS Fargate, ALB and RDS left running.** The production topology, at
  several times the price. Kept as the validated target, not the live host.
- **One EC2 `t4g.small` in Sydney, described in Terraform, running the API,
  worker, website, PostgreSQL, Redis, Caddy and coturn.** Chosen.

## Decision

- **Host:** one `t4g.small` (arm64, 2 vCPU, 2 GB) in `ap-southeast-2`, Ubuntu
  24.04 arm64, a 20 GB encrypted gp3 root volume, IMDSv2 required, an Elastic
  IP and a 2 GB swapfile. Unlike AU-Van it is Terraform:
  `deploy/terraform/live` holds the instance, address, security group, a
  narrow instance role, the media bucket, the backups bucket, the image
  repositories, GitHub's release role and the alarms.
  `deploy/terraform/budget` holds a monthly budget in its own state, so it
  survives a teardown. Agents validate both; the owner applies them.
- **Stack:** `deploy/compose/docker-compose.yml` runs `api`, `worker`, `web`
  (`<ecr_registry>/vetmimi-next`), `postgres:17`, `redis:7`, `caddy:2` and
  `coturn`, with memory limits summing to about 1.5 GB. Caddy serves
  `SITE_DOMAIN` from `web` and `API_DOMAIN` from `api`; until a real domain
  exists both are `sslip.io` names of the Elastic IP. Only Caddy publishes
  ports and only coturn uses the host network.
- **Media:** the API signs S3 requests with the instance role when
  `MEDIA_S3_ACCESS_KEY` is empty, through the AWS SDK's
  `credentials/ec2rolecreds` (the same SDK ADR-001 approves), so no access
  key lives on the host; web sizes under `public/` are read straight from S3.
- **Registry:** private ECR repositories `vetmimi-api` and `vetmimi-next`,
  scanned on push, keeping the last 15 images. GitHub Actions pushes with the
  role `vetmimi-github-release`, assumed through GitHub's OIDC provider and
  trusted only from `main` of the two repositories, so no AWS key and no
  registry token is stored in GitHub or on the host; the host pulls with the
  instance role. Chosen over GHCR, whose private packages need a personal
  token on the host, and to keep the images in the account that runs them.
- **Releases:** images are built in CI, never on the host. After CI passes on
  `main`, `release.yml` builds `linux/arm64` on GitHub's arm runner, pushes
  `:<sha>` and `:main` to ECR, and runs `deploy.sh api <sha>` over SSH. The
  `vetmimi-next` repository releases `web` the same way. `deploy.sh` waits for
  the containers, polls the public health URL for a minute, and on failure
  restores the previous tag and exits 1.
- **Backups:** nightly `pg_dump` to the backups bucket with the instance role,
  kept 30 days there and seven nights on disk.
- **Alarms:** CloudWatch emails the SNS topic `vetmimi-alerts` on a failed
  system status check (and recovers the instance), a failed instance status
  check for 10 minutes (and reboots it), CPU above 80% for 15 minutes, memory
  above 90% and the root disk above 85%. The last two come from the
  CloudWatch agent, limited to those two metrics.
- **Vercel** keeps only pull-request previews of the site.

## Consequences

- It costs about US$19–22 a month, drawn from the credits: the instance
  (about US$15), 20 GB of gp3 (about US$2), the public IPv4 address (about
  US$3.60), five alarms and two custom metrics (about US$1), and S3 and ECR
  storage (cents). When the credits run out the owner chooses again.
- One instance is a single point of failure; a reboot or a release of `api`
  drops open video sessions for a few seconds, and the browser reconnects.
- The instance role is reachable from every container (IMDS hop limit 2), so
  it grants only the two buckets, pulls from the two repositories and the
  agent's namespace, and never deletes a backup.
- An account holds one GitHub OIDC provider per URL. If another project in
  the account (AU-Van) already has one, it is imported, and removed from this
  state before a teardown.
- The site and API share 2 GB. The limits in the compose file are starting
  values to revisit with real usage.
- Vercel-specific features the site uses (if any) must work under
  `next start` in the standalone image; that is the site repository's job.
