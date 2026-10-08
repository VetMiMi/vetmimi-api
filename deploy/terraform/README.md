# Terraform

Each directory is a root module with its own local state (gitignored),
initialised and applied on its own by the owner, never by an agent. CI runs
`terraform fmt -check`, `init -backend=false` and `validate` on every root; that
proves the code is well-formed, not that an apply would succeed.

| Root | What it holds |
|---|---|
| [`budget/`](budget) | The monthly cost budget for resources tagged `Project = vetmimi`. Separate state, so destroying the host never destroys the alarm. Apply it first and leave it |
| [`live/`](live) | The live host of ADR-010: EC2 `t4g.small`, Elastic IP, security group, instance role, media and backups buckets |

The runbook is [`../README.md`](../README.md).

## Conventions

- Provider versions are pinned in each root's `versions.tf`, and each root's
  `.terraform.lock.hcl` is committed with hashes for `darwin_arm64`,
  `linux_amd64` and `linux_arm64`. After changing a version, regenerate it
  with `terraform providers lock -platform=darwin_arm64 -platform=linux_amd64 -platform=linux_arm64`.
- Real values live in `terraform.tfvars`, which is gitignored; each root ships
  a `terraform.tfvars.example` with placeholders only. No account id, email
  address or secret appears in a committed file.
- Every resource carries the provider-level tags `Project`, `Environment` and
  `ManagedBy`.

## Later: the Fargate target

ADR-005's ECS Fargate topology is a separate, later bundle. It will have no
TURN service, because Fargate cannot publish a UDP port range cheaply; a demo
of it either uses the live host's coturn or documents a managed TURN.
