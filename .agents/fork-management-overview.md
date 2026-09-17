# Fork Management Overview

- Goal: separate forward fork development, upstream integration, and shipping release management so each branch has one job.
- Branch roles:
  - `master-azure` is the forward development and integration branch for the fork.
  - `cluster-autoscaler-release-x.y.z-aks` branches are the shipping branches for exact upstream patch bases.

## 1. Develop New Fork Changes

- Start new fork work from `master-azure`.
- Merge the first PR for a new fork change into `master-azure`.
- After merge, cherry-pick the change into each supported AKS release branch that needs it, usually newest supported branch first.
- Use `git cherry-pick -x` and document any manual adaptation or conflict resolution.
- Prefer upstream-first when a change is not fork-specific.
- Related skill: [develop-azure-fork](skills/develop-azure-fork/SKILL.md). This skill should be updated to make `master-azure` the default first target.

## 2. Sync `master-azure` With Upstream

- Run a daily GitHub Actions workflow for `master-azure`.
- If `upstream/master` has no new commits, do nothing.
- If upstream changed, open a sync PR into `master-azure`.
- Let automation attempt the merge and attempt conflict resolution.
- Require the PR to summarize:
  - upstream commit range
  - touched areas
  - conflict files and proposed resolutions
  - validation and Azure e2e results
- Merge promptly when the sync is validated; keep the PR open only when human review is needed.
- Future skills:
  - `sync-master-azure`
  - `verify-master-azure-sync`

## 3. Release New Versions

- New minor version:
  - create the new AKS release branch from the exact upstream tag
  - replay the full set of fork commits from `master-azure`
- New patch version:
  - create the new AKS release branch from the exact upstream patch tag
  - replay the full set of fork commits from the previous AKS release branch for the same minor version
- New revision:
  - keep the same upstream patch base
  - apply only the incremental AKS-only fix on top of the existing AKS release branch
- Only fully supported AKS minor versions receive new release work.
- Use candidate tags first, then official tags after the image build completes.
- Related skill: [manage-azure-ca-releases](skills/manage-azure-ca-releases/SKILL.md). This skill should be updated so new minor releases pull the intended fork delta from `master-azure`.

## 4. Review

- Every sync PR and release PR should include the upstream base or commit range, the exact commit set, resolved conflicts, and validation results.
- Use [verify-azure-ca-release](skills/verify-azure-ca-release/SKILL.md) for release review.
- Use [verify-azure-fork-patch-commit](skills/verify-azure-fork-patch-commit/SKILL.md) for the main fork delta commit.
- Future sync review should use a dedicated `verify-master-azure-sync` skill.

## 5. Skills

- Existing:
  - [develop-azure-fork](skills/develop-azure-fork/SKILL.md)
  - [manage-azure-ca-releases](skills/manage-azure-ca-releases/SKILL.md)
  - [verify-azure-ca-release](skills/verify-azure-ca-release/SKILL.md)
  - [verify-azure-fork-patch-commit](skills/verify-azure-fork-patch-commit/SKILL.md)
- Future:
  - `sync-master-azure`
  - `verify-master-azure-sync`

## Operating Principles

- `master-azure` is the main place for new fork development and upstream integration.
- Release branches are the source of truth for shipped versions.
- Do not start releases from `master-azure`; start from exact upstream tags and replay the intended fork history.
- Do not merge `master-azure` wholesale into release branches; use traceable cherry-picks or replayed commits.