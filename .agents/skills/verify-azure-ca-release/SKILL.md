---
name: verify-azure-ca-release
description: 'Verify Azure Cluster Autoscaler fork release PRs for correctness. Use when: reviewing an AKS release PR, checking a new minor version release, checking a new patch version release, checking a revision on an existing AKS release branch, confirming support eligibility, validating upstream tag ancestry, checking candidate and official tags, or auditing master-azure backports.'
---

# Verify Azure CA Release PRs

## Goal

Turn release review into a repeatable pass or fail checklist.

A correct review should answer:

- Is the target AKS minor version still fully supported?
- Is the release branch anchored to the correct upstream tag?
- Does the PR contain only the intended AKS commits?
- Are cherry-picks traceable and justified?
- Are all resolved conflicts documented clearly enough to reconstruct the decision later?
- Are the candidate and official tag names correct for this release type?
- Did the required validation run?

Return findings in this order:

1. blockers
2. risks or missing evidence
3. concise pass summary

## Reviewer Inputs

Ask the PR author to provide these facts in the PR description if they are not already present:

- release type: new minor version, new patch version, or revision
- target release branch
- upstream base tag
- candidate tag name
- official tag name
- list of cherry-picked commits with source SHAs
- every resolved conflict, including affected files and rationale
- explanation for any `master-azure`-derived commits
- validation commands run and results

If any of these are missing, report that as a review gap.

## Review Workflow

### 1. Identify the PR Shape

Confirm:

- base branch is the AKS release branch, for example `cluster-autoscaler-release-1.35.2-aks`
- head branch is a separate working branch
- the PR is not targeting `master-azure` or `master`

If reviewing the active or checked out PR, gather:

- title
- base branch
- head branch
- changed files
- commit list

Fail if the PR is opened against the wrong base branch.

### 2. Check Support Eligibility

Only fully supported AKS minor versions are eligible for new fork releases or new fork commits.

Use both sources:

```bash
az aks get-versions --location <region> --output table
```

- https://learn.microsoft.com/en-us/azure/aks/supported-kubernetes-versions?tabs=azure-cli

Fail if the target minor version is already in LTS-only support, AKS platform support (`N-3`), or outside support.

### 3. Verify the Upstream Base

For a new minor version or patch version release, the AKS release branch must start at the exact upstream tag before the PR commits are merged.

Example for `cluster-autoscaler-release-1.35.2-aks`:

```bash
git fetch upstream --tags origin
git rev-parse cluster-autoscaler-1.35.2
git rev-parse origin/cluster-autoscaler-release-1.35.2-aks
git rev-list --left-right --count cluster-autoscaler-1.35.2...origin/cluster-autoscaler-release-1.35.2-aks
```

Expected before merge:

- the release branch already exists and is pushed
- the release branch matches the upstream tag exactly, or the author provides a precise explanation for any divergence

For a revision PR, verify instead that the base branch is the existing AKS release branch for the same upstream patch version.

Fail if the release branch was created from `master-azure`, the previous AKS release branch, or any base other than the intended upstream tag.

### 4. Verify the PR Commit Set

Inspect only the commits introduced by the PR branch:

```bash
git log --oneline origin/<release-branch>..HEAD
git diff --stat origin/<release-branch>...HEAD
git log --merges --oneline origin/<release-branch>..HEAD
```

Check that:

- the PR contains only the intended AKS commits
- there are no merge commits from `master-azure`
- the commit list matches the PR description
- any non-obvious extra commit is explained

Fail if the PR drags in unrelated commits, broad sync noise, or merge commits that hide provenance.

### 5. Verify Cherry-Pick Provenance

For every replayed commit, prefer `git cherry-pick -x` so the commit message records the source SHA.

Check the commit bodies for provenance lines such as:

```text
(cherry picked from commit <sha>)
```

If a commit does not use `-x`, require the PR description to explain its origin.

Fail if the reviewer cannot map a release-branch commit back to its source.

### 6. Verify Conflict Documentation

Any resolved conflict must be documented in the PR description or in the commit that resolved it.

For each resolved conflict, expect:

- affected file or files
- the competing change being reconciled
- the chosen resolution
- why that resolution is correct for this release line
- whether the resolution intentionally changes behavior

Short notes such as `basic conflict resolution` are not enough by themselves.

Fail if the PR required manual conflict resolution or adaptation and the rationale is undocumented.

### 7. Verify master-azure-Derived Commits

`master-azure` is only a source of candidate backports, not a release base.

If the PR includes commits that originated on `master-azure`, verify that the author documented:

- why the commit is needed on this release line
- why it is compatible with this release line
- whether it was applied unchanged or adapted

Suggested review command:

```bash
git log --no-merges upstream/master..master-azure --oneline
```

Fail if `master-azure` is used as a merge source or if the compatibility justification is missing.

### 8. Verify Tags

The tag scheme is part of correctness.

Initial release:

- candidate tag: `vX.Y.Z-aks-1-candidate`
- official tag: `vX.Y.Z-aks-1`

Later revision release:

- candidate tag: `vX.Y.Z-aks-N-candidate`
- official tag: `vX.Y.Z-aks-N`

The initial release starts at `N=1`, and each later respin increments `N`.

Review checks:

- the candidate tag name matches the version and release number
- the candidate tag points at the PR commit being reviewed
- the official tag is not used prematurely while the candidate is still under validation

Useful commands:

```bash
git tag --points-at <pr-head-sha>
git rev-parse <candidate-tag>
```

Fail if the candidate tag name is wrong, the tag points at the wrong commit, or the official tag has been applied before the release is finalized.

### 9. Verify Validation Evidence

At minimum, reviewers should expect evidence for the same core validation entry points used in the release workflow:

```bash
hack/verify-all.sh -v
```

```bash
cd cluster-autoscaler && go test -v -coverprofile=coverage.txt -covermode=atomic -race $(go list ./... | grep -v vertical-pod-autoscaler/e2e | grep -v /cloudprovider/ && go list ./cloudprovider/azure/...)
```

If the change affects Azure behavior, expect targeted Azure-specific validation as well.

Do not rely on `make release-validate` for this AKS fork flow.

Fail if required validation was not run, if results are missing, or if failures are unexplained.

## Reviewer-Friendly PR Structure

To make review faster, ask authors to include a short release manifest in the PR body:

```text
Release type: new patch version
Target branch: cluster-autoscaler-release-1.35.2-aks
Upstream tag: cluster-autoscaler-1.35.2
Candidate tag: v1.35.2-aks-1-candidate
Official tag: v1.35.2-aks-1
Cherry-picks:
- <sha> main AKS fork delta
- <sha> follow-up fix
Resolved conflicts:
- none
master-azure-derived commits:
- none
Validation:
- hack/verify-all.sh -v
- Azure go test command
```

If this manifest is missing, recommend adding it before full review.

## Review Output

When you finish the verification, summarize with a flat checklist:

- support eligibility: pass or fail
- upstream base: pass or fail
- PR commit set: pass or fail
- cherry-pick provenance: pass or fail
- resolved conflicts documented: pass or fail
- tag correctness: pass or fail
- validation evidence: pass or fail

Then list blockers first. If there are no blockers, state that explicitly and mention any residual risk or missing evidence.
