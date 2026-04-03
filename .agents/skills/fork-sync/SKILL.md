---
name: fork-sync
description: 'Sync Azure/autoscaler fork master with upstream kubernetes/autoscaler master. Use when: syncing fork, updating master from upstream, fast-forwarding Azure fork, rebasing with upstream, pulling upstream changes.'
argument-hint: 'Optional: specify upstream ref (default: upstream/master)'
---

# Fork Sync: Azure/autoscaler ← kubernetes/autoscaler

Syncs `origin/master` (Azure/autoscaler) with `upstream/master` (kubernetes/autoscaler) by creating a PR branch based on upstream with Azure-only commits cherry-picked on top.

## When to Use

- Azure/autoscaler master is behind upstream/kubernetes/autoscaler master
- Before creating a new AKS release branch (to ensure master is current)
- Periodic sync to keep fork up to date

## Prerequisites

- Remotes configured:
  - `origin` → `https://github.com/Azure/autoscaler`
  - `upstream` → `https://github.com/kubernetes/autoscaler.git`
- `gh` CLI authenticated with push access to Azure/autoscaler

## Procedure

### 1. Fetch latest from both remotes

```bash
git fetch upstream
git fetch origin
```

### 2. Assess divergence

Identify Azure-only commits (commits on `origin/master` not on `upstream/master`):

```bash
git log --oneline origin/master --not upstream/master
```

Typical Azure-only commits:
- `Microsoft mandatory file` (SECURITY.md) — must be preserved
- `Auto merge mandatory file pr` — merge commit, can be skipped (content already in the above)

Count upstream commits to sync:

```bash
git log --oneline upstream/master --not origin/master | wc -l
```

### 3. Create sync branch from upstream/master

```bash
git checkout -b sync/upstream-master-to-azure upstream/master
```

### 4. Cherry-pick Azure-only commits

Cherry-pick only the **non-merge** Azure-specific commits. Skip merge commits (they have no unique content if their parents are included).

```bash
# List Azure-only non-merge commits
git log --oneline --no-merges origin/master --not upstream/master

# Cherry-pick each
git cherry-pick <sha>
```

Currently the only Azure-specific commit is the Microsoft mandatory `SECURITY.md` file.

### 5. Verify the result

The branch should differ from `upstream/master` only by Azure-specific files:

```bash
git diff upstream/master --name-only
# Expected: only SECURITY.md (or other Azure-mandatory files)
```

Compare against `origin/master` for the full sync scope:

```bash
git diff --stat origin/master
```

### 6. Run unit tests

Build and run unit tests to verify the sync doesn't break anything:

```bash
cd cluster-autoscaler
go build ./...
make test-unit
```

All packages should pass. If there are failures, investigate whether they are pre-existing upstream issues or caused by the Azure-specific cherry-picks.

### 7. Push and create PR

```bash
git push origin sync/upstream-master-to-azure --no-verify
gh pr create \
  --repo Azure/autoscaler \
  --base master \
  --head sync/upstream-master-to-azure \
  --title "chore: sync Azure/autoscaler master with upstream" \
  --body "Syncs origin/master with upstream/master (kubernetes/autoscaler).

Azure-only commits (SECURITY.md) cherry-picked on top of upstream/master.

## Divergence
- Upstream commits synced: <count>
- Azure-only commits preserved: <count>
- Files changed vs origin/master: <count>"
```

## Important Notes

- **Do NOT use `git merge`** — this creates a merge commit that pollutes the history. Cherry-pick keeps it clean.
- **Merge commits are skipped** — they contain no unique changes if their parent commits are included.
- **Author is preserved** — `git cherry-pick` keeps the original Author field; only Committer changes.
- **Order matters with fork cherry-pick PR** — if a fork cherry-pick PR (e.g., AKS fork differences) is pending, decide whether to merge the sync first or the fork first. Merging sync first means the fork cherry-pick needs re-doing against the new master.
- **`--no-verify` on push** — needed if git-lfs hooks are configured but `git-lfs` is not installed.
