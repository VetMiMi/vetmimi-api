# The live host

The owner's runbook for the one EC2 instance that serves the API and the
website ([ADR-010](../docs/adr/010-live-host-on-ec2-with-the-website.md)).

**Every step here belongs to the owner.** Agents write and validate these
files; they never run `terraform apply` or `destroy`, an `aws` command, `ssh`
to the host, or create a secret. No value from the host's env files ever goes
into a chat, a commit or a workflow log.

The code blocks carry no `#` comments, because an interactive zsh passes them
to the command as arguments.

## What is where

| Path | What it is |
|---|---|
| `terraform/budget/` | The monthly cost budget, its own state, applied once and never destroyed |
| `terraform/live/` | The instance, Elastic IP, security group, instance role, the two buckets, the two image repositories, GitHub's release role and the alarms |
| `bootstrap.sh` | Prepares a fresh Ubuntu host; safe to re-run |
| `deploy.sh` | `deploy.sh <api\|web> <sha>`: releases one image, rolls back if unhealthy |
| `backup.sh` | The nightly `pg_dump` to the backups bucket |
| `compose/docker-compose.yml` | `api`, `worker`, `web`, `postgres`, `redis`, `caddy`, `coturn` |
| `compose/caddy/Caddyfile` | `SITE_DOMAIN` → `web:3000`, `API_DOMAIN` → `api:8080` |
| `compose/coturn/turnserver.conf` | coturn, hardened; holds no secret |
| `Dockerfile` | The API image, built only by CI |

On the host the repository is cloned to `/srv/vetmimi`, owned by `deploy`:

| Host file | Holds |
|---|---|
| `/srv/vetmimi/deploy/compose/.env` | The host's values, from `compose/.env.example` |
| `/srv/vetmimi/deploy/compose/api.env` | The API's own values, from the repository's `.env.example` |
| `/srv/vetmimi/deploy/compose/.env.tags` | `API_TAG` and `WEB_TAG`, the last good releases; written by `deploy.sh` only |
| `/srv/vetmimi/backups/` | The newest seven nightly dumps |

Memory limits for the 2 GB instance, as starting values: PostgreSQL 448 MB,
web 384 MB, api 192 MB, worker 192 MB, Redis 96 MB, Caddy 96 MB, coturn 96 MB.
Check `docker stats` after a week of real use and adjust.

## 1. Apply the budget

The budget counts resources tagged `Project = vetmimi`, so the AU-Van host in
the same account does not trip it. Fill `notification_email` first.

```sh
export AWS_PROFILE=auvan
cp deploy/terraform/budget/terraform.tfvars.example deploy/terraform/budget/terraform.tfvars
terraform -chdir=deploy/terraform/budget init
terraform -chdir=deploy/terraform/budget apply
```

After step 2, once the `Project` tag shows in Billing (up to a day), activate
it under Billing and Cost Management, Cost allocation tags. Until then the
budget sees nothing.

If `auvan` is an `aws login` session that Terraform cannot read, add a profile
to `~/.aws/config` that asks the session for fresh credentials, and use
`AWS_PROFILE=auvan-tf` instead:

```ini
[profile auvan-tf]
credential_process = aws configure export-credentials --profile auvan --format process
```

## 2. Apply the live host

Put your SSH public key and the address for alarms in `terraform.tfvars`.

```sh
cp deploy/terraform/live/terraform.tfvars.example deploy/terraform/live/terraform.tfvars
terraform -chdir=deploy/terraform/live init
terraform -chdir=deploy/terraform/live apply
terraform -chdir=deploy/terraform/live output
```

The outputs are the values the next steps ask for. The instance has
termination protection, because the database lives on its disk.

AWS emails `alert_email` a subscription confirmation for the `vetmimi-alerts`
topic; click it, or no alarm reaches you.

An account holds one GitHub OIDC provider for
`token.actions.githubusercontent.com`. If the apply fails because it already
exists (AU-Van added one), import it instead and apply again:

```sh
terraform -chdir=deploy/terraform/live import aws_iam_openid_connect_provider.github arn:aws:iam::<account id>:oidc-provider/token.actions.githubusercontent.com
terraform -chdir=deploy/terraform/live apply
```

An imported provider is shared: see Teardown before destroying.

If the apply stops at the media bucket policy with `AccessDenied`, the
account blocks public bucket policies: allow them for this bucket under S3,
Block Public Access settings for this account, or the website's images will
not load.

## 3. Bootstrap the host

```sh
ssh ubuntu@<public_ip>
curl -fsSLO https://raw.githubusercontent.com/VetMiMi/vetmimi-api/main/deploy/bootstrap.sh
sudo bash bootstrap.sh
```

It installs Docker and the compose plugin, the AWS CLI, the CloudWatch agent
(memory and root disk every five minutes), a 2 GB swapfile and unattended
upgrades, creates the `deploy` user in the `docker` group, clones this
repository to `/srv/vetmimi`, and installs the nightly backup in
`/etc/cron.d/vetmimi-backup`.

The host needs no registry password: `deploy.sh` logs in to ECR with the
instance role before each pull.

## 4. Fill the two env files

As `deploy`, with values generated on the host, never on a laptop:

```sh
sudo -iu deploy
cd /srv/vetmimi/deploy/compose
umask 077
cp .env.example .env
cp /srv/vetmimi/.env.example api.env
nano .env api.env
```

In `.env`: `ECR_REGISTRY` from the output `ecr_registry`; `SITE_DOMAIN` and
`API_DOMAIN` from `site_domain` and `api_domain`; `TURN_EXTERNAL_IP` from
`public_ip`; `BACKUP_BUCKET` from `backups_bucket`; and `openssl rand -hex 32` for `POSTGRES_PASSWORD`,
`SERVICE_KEY`, `SITE_REVALIDATE_SECRET` and `TURN_SECRET`.

In `api.env`: `SIGNING_SECRET` (`openssl rand -hex 32`), `TOTP_ENCRYPTION_KEY`
(`openssl rand -base64 32`), `RESEND_API_KEY`, `EMAIL_FROM`,
`MEDIA_S3_ENDPOINT`, `MEDIA_S3_BUCKET` and `MEDIA_PUBLIC_URL` from the outputs,
`MEDIA_S3_REGION=ap-southeast-2`, and the optional Meta, LinkedIn and
Anthropic values. Leave `MEDIA_S3_ACCESS_KEY` and `MEDIA_S3_SECRET_KEY` empty:
the instance role grants the bucket. The compose file sets `ENV`, `PORT`,
`DATABASE_URL`, `REDIS_URL`, `PUBLIC_API_URL`, `SITE_URL`, `SERVICE_KEY`,
`SITE_REVALIDATE_SECRET`, `TURN_HOST` and `TURN_SECRET` itself, from `.env`;
whatever `api.env` says for them is ignored.

## 5. Connect GitHub to the host

Each repository gets its own key, so either can be revoked alone. For each of
`vetmimi-api` and `vetmimi-next`:

1. On your laptop: `ssh-keygen -t ed25519 -N '' -C vetmimi-<repo>-deploy -f vetmimi-<repo>-deploy`.
2. On the host, append the public key to `/home/deploy/.ssh/authorized_keys`
   on one line, bound to the deploy script:

   ```text
   command="/srv/vetmimi/deploy/deploy.sh",restrict ssh-ed25519 AAAA... vetmimi-<repo>-deploy
   ```

   `restrict` turns off forwarding and terminals. The client's command,
   `api <sha>` or `web <sha>`, reaches `deploy.sh` as `SSH_ORIGINAL_COMMAND`,
   which refuses anything but `api` or `web` and a full 40-character sha.
3. On the host, print the host key line to pin. Reading it there, not with
   `ssh-keyscan` from elsewhere, is what makes the pin trustworthy:
   `awk -v h=<public_ip> '{print h, $1, $2}' /etc/ssh/ssh_host_ed25519_key.pub`.
4. In the repository's settings, create the environment `production` limited
   to `main`, with three secrets: `DEPLOY_HOST` (the public IP),
   `DEPLOY_SSH_KEY` (the private key file) and `DEPLOY_KNOWN_HOSTS` (the line
   from step 3). Then delete the private key from your laptop.
5. Under Secrets and variables, Actions, Variables, add the repository
   variable `AWS_RELEASE_ROLE_ARN` with the output `release_role_arn`. It is
   not a secret, and it is a repository variable, not an environment one,
   because the image job runs outside `production`.

The role trusts only workflows on `main` of the two repositories and may
only push to their two image repositories; no AWS key is stored in GitHub.
Until `AWS_RELEASE_ROLE_ARN` is set, `release.yml` skips the push and the
deploy with a notice; until `DEPLOY_HOST` is set, it pushes the image and
skips the deploy.

## 6. First release

Release the API first (Caddy waits for it), then the website. The image for
the sha must already be in ECR: re-run `Release` from the Actions tab after
step 5, or release by hand with the newest sha of `main`:

```sh
sudo -iu deploy
cd /srv/vetmimi
git fetch origin main
deploy/deploy.sh api "$(git rev-parse origin/main)"
```

The website's sha comes from `vetmimi-next`'s release, or run
`deploy/deploy.sh web <full sha of vetmimi-next main>` after its image exists.
Caddy obtains both certificates on first start, so the first health polls may
fail for a few seconds.

Create the first administrator; it asks for a password and prints the TOTP
URI, so it needs a terminal:

```sh
cd /srv/vetmimi/deploy/compose
docker compose --env-file .env --env-file .env.tags run --rm --no-deps api \
  --mode create-user --email <you> --name "<Your Name>" --roles site_admin --practitioner
```

Then check `https://<site_domain>` and `https://<api_domain>/readyz`.

## Releases and rollback

Each merge to `main` releases itself: CI, then `release.yml` builds
`<ecr_registry>/vetmimi-api:<sha>` for arm64 and runs `deploy.sh api <sha>`.
`deploy.sh` logs in to ECR, checks the sha out (so the compose files match the image), pulls,
starts `api` and `worker` and waits for them to report healthy, reloads Caddy
(a reload, so open video sessions survive), and polls
`https://$API_DOMAIN/healthz` every two seconds for about a minute. If it never
answers, it restores the previous tag in `.env.tags`, starts that release
again and exits 1, which fails the workflow. With no previous release it stops
the new one instead. `web` is the same against `https://$SITE_DOMAIN/api/health`.

To roll back by hand, release an earlier sha:

```sh
sudo -iu deploy
cd /srv/vetmimi
git log --oneline -10 origin/main
deploy/deploy.sh api "$(git rev-parse <short sha>)"
```

**Rollback swaps the image, not the schema.** Every migration must work with
the release before it: add a column first, stop using the old one in a later
release, drop it in a third. A migration that breaks the previous release
makes rollback useless; restore a backup instead.

A change to `coturn/turnserver.conf` or the compose file's `coturn` service
needs `docker compose --env-file .env --env-file .env.tags up -d --force-recreate coturn`;
a Caddyfile change is picked up by the next release's reload.

## Backups and restore

At 16:30 UTC (02:30 or 03:30 in Sydney), `backup.sh` writes a custom-format
`pg_dump`, checks it with `pg_restore --list`, uploads it to
`s3://<backups bucket>/postgres/` with the instance role, and keeps the newest
seven in `/srv/vetmimi/backups/`. S3 expires each dump after 30 days. It logs
to syslog as `vetmimi-backup`: `journalctl -t vetmimi-backup`.

The bucket holds personal data. It is private, encrypted, unversioned, and the
instance role can write and read it but never delete; only the lifecycle rule
removes dumps.

To restore, as `deploy` on the host:

1. Pick a dump: the newest local one, or fetch one from the bucket.

   ```sh
   cd /srv/vetmimi/deploy/compose
   aws s3 ls s3://<backups bucket>/postgres/
   aws s3 cp s3://<backups bucket>/postgres/<file>.dump /srv/vetmimi/backups/
   ```

2. Restore it into a scratch database and compare it with the live one.

   ```sh
   alias dc='docker compose --env-file .env --env-file .env.tags'
   dc exec -T postgres createdb -U vetmimi vetmimi_restore
   dc exec -T postgres pg_restore --no-owner -U vetmimi -d vetmimi_restore < /srv/vetmimi/backups/<file>.dump
   dc exec -T postgres psql -U vetmimi -d vetmimi_restore -c 'select (select count(*) from appointments) appointments, (select count(*) from communications) communications, (select count(*) from users) users'
   ```

3. Only to replace the live data: stop the writers, restore over the live
   database, start them, and check `/readyz`.

   ```sh
   dc stop api worker
   dc exec -T postgres pg_restore --clean --if-exists --no-owner -U vetmimi -d vetmimi < /srv/vetmimi/backups/<file>.dump
   dc start api worker
   curl -fsS https://<api_domain>/readyz
   ```

4. Drop the scratch database: `dc exec -T postgres dropdb -U vetmimi vetmimi_restore`.

### Restore rehearsal

Launch (M5) needs one recorded rehearsal. Each time:

- [ ] The newest dump in the bucket is from last night (`aws s3 ls`).
- [ ] Fetch it and restore it into `vetmimi_restore` (step 2).
- [ ] Row counts of `appointments`, `communications` and `users` match the
      live database within a day's changes.
- [ ] Drop `vetmimi_restore`.
- [ ] Add a row below.

| Date | Dump used | Duration | Who |
|---|---|---|---|
| | | | |

## Alarms

CloudWatch emails the `vetmimi-alerts` topic when an alarm fires and again
when it clears (`terraform/live/alarms.tf`):

| Alarm | Fires when | What happens, what to do |
|---|---|---|
| `vetmimi-live-system-status` | AWS's host fails its check for 2 minutes | EC2 recovers the instance onto new hardware, same IP and disk. Check the site once it clears |
| `vetmimi-live-instance-status` | The OS fails its check for 10 minutes | EC2 reboots it. If it repeats, look for out-of-memory kills: `journalctl -k \| grep -i oom` |
| `vetmimi-live-cpu` | CPU above 80% for 15 minutes | `docker stats`; sustained load spends the t4g's CPU credits |
| `vetmimi-live-memory` | Memory above 90% for 10 minutes | `docker stats`; revisit the compose memory limits |
| `vetmimi-live-disk` | Root disk above 85% | `docker system df`, `docker image prune -a`, or raise `root_volume_gb` |

Memory and disk come from the CloudWatch agent (`bootstrap.sh`, namespace
`VetMiMi`). They show insufficient data until bootstrap has run, and again if
the agent stops: `systemctl status amazon-cloudwatch-agent`.

## A video session will not connect

- `docker compose --env-file .env --env-file .env.tags logs coturn | grep 401`:
  a `401` means the API and coturn disagree on `TURN_SECRET`. Both read it from
  `.env`, so one container is stale; recreate both.
- The security group admits 3478 TCP and UDP and 49160–49200 UDP
  (`terraform/live/network.tf`); the relay range must match
  `turnserver.conf`.
- `TURN_EXTERNAL_IP` must be the Elastic IP: EC2 maps it 1:1 to a private
  address, and coturn has to advertise the public one.
- In the browser, `chrome://webrtc-internals` should show a `relay` candidate
  pair for a call over mobile data.

## Change the host names

When a real domain exists, point an A record for the site and one for the API
at the Elastic IP, set `SITE_DOMAIN` and `API_DOMAIN` in `.env`, and release
`api` and then `web` again: Caddy obtains the new certificates and the API's
URLs follow. `DEPLOY_HOST` and its pin name the IP, so they do not change.

## Teardown

Only when the practice leaves this host. Take a final backup and copy it off
first. Then turn off termination protection, empty both buckets (they refuse
to be destroyed while they hold objects, on purpose), and destroy. The image
repositories are deleted with their images, which can be rebuilt. If the
GitHub OIDC provider was imported in step 2, first run
`terraform -chdir=deploy/terraform/live state rm aws_iam_openid_connect_provider.github`
so the destroy leaves AU-Van's provider alone.

```sh
terraform -chdir=deploy/terraform/live apply -var termination_protection=false
aws s3 rm s3://<media bucket> --recursive
aws s3 rm s3://<backups bucket> --recursive
terraform -chdir=deploy/terraform/live destroy
```

Leave `terraform/budget` applied.
