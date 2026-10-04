---
description: Run one full vetmimi-api delivery cycle — plan, implement, CI, merge, board — for the next ready issue.
---

Run one complete delivery cycle for the next ready `vetmimi-api` issue. The
same command exists in `vetmimi-next`; the orchestrator runs both and
sequences cross-repository work (API first, tagged; then the site).

## Concurrency and the 8 GB rule

At most **two tracks** run at once across both repositories, and only when
their file surfaces are disjoint. Heavy commands already serialise through
`scripts/gate.sh`; the two-track cap is about memory and about merge
conflicts. Shared surfaces that force serialisation:

- `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.github/`, `.gitignore`
- `openapi.yaml` and the generated `internal/httpapi/gen/`, `internal/db/`
- `migrations/` (sequence numbers) and `docs/data-model.md`
- `docs/project-status.md` — orchestrator-owned, implementers never touch it
- `docs/adr/` numbers — the orchestrator allocates them

Safe pairings: an API domain issue with a `vetmimi-next` admin screen whose
endpoints already exist on `main`; a `deploy/` issue with anything.

Each track rebases on `main` after any other track merges, then re-runs CI.

## 1. Sync and select

```sh
git checkout main && git pull --ff-only
gh issue list --state open --json number,title,labels,milestone
gh pr list --state open
```

Take the next issue from the ordered backlog in `docs/project-status.md`.
Verify the previous pull request merged before branching (PRs here squash,
so old branches are stale).

## 2. Plan

Set the board item to **In progress**:

```sh
gh project item-edit --project-id PVT_kwDOEzlLUc4Bia-x --id <item-id> \
  --field-id PVTSSF_lADOEzlLUc4Bia-xzhhTG6o --single-select-option-id e7394687
```

Run the `issue-planner` agent. `CREDENTIALS-REQUIRED` → **Blocked**.
`ADR-REQUIRED` → allocate the next ADR number and tell the implementer.
Post the plan: `gh issue comment <n> --body-file <plan>`.

## 3. Implement

```sh
git worktree add -b <type>/<kebab-description> .worktrees/<short-name> main
```

Run the `implementer` agent and **pass it `.worktrees/<short-name>`** in the
prompt; it does not read this file. It commits subject-only Conventional
Commits and runs `make gate` in the worktree before pushing.

## 4. Open the pull request

```sh
git push -u origin <branch>
gh pr create --base main --head <branch> --title "<type(scope): outcome>" --body-file <body>
gh project item-add 1 --owner VetMiMi --url <pr-url>
```

Name `--head` explicitly: the root tree stays on `main`. The title must be a
valid Conventional Commit because it becomes the squash commit. Fill the
template, including `Closes #n`. No tool attribution anywhere. Set the PR's
board item to **In review** (`856f26e8`), with milestone, labels and the
Area/Priority/Size fields.

## 5. CI gate

```sh
gh pr checks <n> --watch
```

"API checks", "Deployment checks" and "Commit checks" must all pass. Red →
**Failure handling**. There is no separate review step: the owner chose to
merge on green.

## 6. Merge

```sh
gh pr view <n> --json mergeable,mergeStateStatus
gh pr merge <n> --squash --delete-branch
```

Then `git worktree remove .worktrees/<short-name>`.

## 7. Close out

Set the issue's and the PR's board items to **Done** (`1c14b8a1`); close the
issue if `Closes #` did not. If the change altered `openapi.yaml`, tag the
merge (`git tag v0.<minor>.<patch> && git push --tags`) so `vetmimi-next` can
pin it. Then **read the board back** and confirm the statuses.

Update `docs/project-status.md` (current state, last merged commit, backlog,
blockers) on a `docs/status-<short-name>` branch and land it through steps
4–6; it is the one pull request without its own issue.

## Failure handling

Counters are per issue and reset on merge.

- **CI red.** Infrastructure flake → `gh run rerun --failed` once, uncounted.
  Real failure → the implementer fixes it on the same branch. **Limit 3.**
- **Conflict with `main`.** `cd .worktrees/<short-name> && git fetch origin &&
  git rebase origin/main`, then `git push --force-with-lease`, then CI again.
  A conflict in `migrations/` or in scheduling code → **Blocked**; never guess
  at merge semantics for time ranges.
- **`main` goes red after a merge.** Highest priority. Fix forward in one
  cycle or `git revert --no-commit <sha>` with a `revert(scope): …` subject.
- **Caps.** Two hours of wall clock on one issue → **Blocked**.

Never merge on a red or pending check, never weaken a test, never edit the
loop's own rules to get unblocked.

## Blocked

Stop only for: credentials needed, an irreversible or cost-incurring action,
retry caps exhausted, an empty backlog, or a product decision only Daw Mi or
the owner can make. Then:

1. Comment the question and the options on the issue.
2. Set the board item back to **In progress** and add the `status: blocked`
   label.
3. Write it into **Current blockers** in `docs/project-status.md` (landed as
   in step 7) so a fresh session recovers it.
4. Report to the owner, once, with the question and your recommendation.
