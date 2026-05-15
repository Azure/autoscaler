---
name: manage-azure-ca-releases
description: 'Manage Azure Cluster Autoscaler fork releases - new minor versions, new patch versions, and small revisions on existing AKS release branches. Use when: cutting a new AKS release from an upstream tag, creating and pushing the next AKS release branch for an upstream patch version, opening a PR from a separate branch with the AKS fork commits, revising an existing AKS release branch with a small change, checking whether a target branch is still fully supported, or evaluating whether master-azure-only commits should be cherry-picked into supported release branches.'
---

# Azure CA Release Workflow

## Core Policy

- Shipping branches are `cluster-autoscaler-release-x.y.z-aks`.
- Releases are always built from:
  1. the exact upstream `cluster-autoscaler-x.y.z` tag,
  2. the main AKS fork delta commit for that release line,
  3. any additional approved AKS fork commits.
- For a new minor version or patch version release:
  1. create the AKS release branch at the exact upstream tag and push it,
  2. create a separate working branch from that release branch,
  3. open a PR from the working branch back into the release branch with the AKS commits.
- This skill covers the Git workflow and release tags for this repo.
- Tag the PR commit with a `-candidate` tag first, build an image from that candidate tag, then add the official tag after the image is fully built.
- Do not cut releases from `master-azure`.
- `master-azure` is only a source of candidate Azure-only commits that may need to be backported.
- Any resolved conflicts must be documented in the PR description or in the commit that resolved them.
- Only fully supported AKS minor versions receive new fork releases or new fork commits.
- If a minor version is now in AKS LTS-only support, AKS platform support (`N-3`), or is otherwise outside the fully supported GA window, stop and do not create a new minor version, patch version, or revision release for it.

For this fork, treat "eligible for new releases" as stricter than "AKS still offers some support". The release target must still be fully supported, not merely available through LTS or platform support.

## Recent Examples

Use the recent Bevan-led AKS patch version releases as the model for new versions:

- `1.32.7-aks`: release branch `cluster-autoscaler-release-1.32.7-aks` was created at upstream tag `cluster-autoscaler-1.32.7`, then PR `#22` from `Azure/theunrepentantgeek/v1.32.7` added the AKS commits.
- `1.33.4-aks`: upstream release point `cluster-autoscaler-1.33.4`, then PR `#25` from `Azure/theunrepentantgeek/cluster-autoscaler-1.33.4` added the AKS commits.
- `1.34.3-aks`: upstream release point `cluster-autoscaler-1.34.3`, then PR `#31` from `Azure/theunrepentantgeek/cluster-autoscaler-1.34.3` added the AKS commits.

Follow that same two-step pattern for future new minor version and patch version releases.

## Support Gate

Check support status before you create a branch, cherry-pick a fix, or revise an existing release branch.

Use both sources:

```bash
az aks get-versions --location <region> --output table
```

- AKS support policy and release calendar:
  - https://learn.microsoft.com/en-us/azure/aks/supported-kubernetes-versions?tabs=azure-cli

Apply the following rule:

- Only target AKS minor versions that are still in the fully supported GA window.
- Do not target minor versions that have moved into AKS LTS-only support.
- Do not target minor versions that have moved into AKS platform support (`N-3`).
- Do not target minor versions that are outside support.

As of May 2026, that means you must verify whether `1.33` is still fully supported before touching it. If it has already moved out of the GA support window, do not backport new fork commits there even if older plans referenced `1.33-1.35`.

## Naming

- Release branches: `cluster-autoscaler-release-x.y.z-aks`
- Initial candidate tag: `cluster-autoscaler-release-x.y.z-aks-candidate`
- Initial official tag: `cluster-autoscaler-release-x.y.z-aks`
- Revision candidate tag: `cluster-autoscaler-release-x.y.z-aks-N-candidate`
- Revision official tag: `cluster-autoscaler-release-x.y.z-aks-N`
- Older tags such as `v1.31.4-aks-1` exist in the repo, but new AKS release tags should use the branch-name-based scheme above.

Keep the branch name tied to the upstream patch version base. If you need a small respin on top of the same upstream patch version, keep the branch name and use a new revision tag.

## Release Types

### New Minor Version

Use this when AKS adopts a new upstream minor version and that minor version is still fully supported.

1. Verify that the target minor version is fully supported.
2. Fetch upstream tags.
3. Create the AKS release branch from the exact upstream release tag, not from `master-azure` and not from the prior AKS branch.

```bash
git fetch upstream --tags
git switch -c cluster-autoscaler-release-1.36.0-aks cluster-autoscaler-1.36.0
git push -u origin cluster-autoscaler-release-1.36.0-aks
```

4. Create a separate working branch from the just-pushed AKS release branch.

```bash
git switch -c <topic-branch> cluster-autoscaler-release-1.36.0-aks
```

5. Cherry-pick the main AKS fork delta commit for that line on the working branch.
6. Cherry-pick any additional approved AKS fork commits after the main fork delta commit on the working branch.
7. If any cherry-pick needs manual conflict resolution, document every resolved conflict in the PR description or in the resolving commit, including the affected files and rationale.
8. Evaluate whether any `master-azure`-only compatibility commits must also be applied on the working branch.
9. Run validation on the working branch.
10. Open a PR from the working branch into `cluster-autoscaler-release-1.36.0-aks`.
11. Tag the PR commit with `cluster-autoscaler-release-1.36.0-aks-candidate`.
12. Build an image from that candidate tag.
13. After the PR lands and the image from that candidate tag is fully built, add the official tag `cluster-autoscaler-release-1.36.0-aks`.

Do not merge `master-azure` forward to create a new minor version release.

### New Patch Version

Use this when upstream publishes a new patch version for an already supported minor version and you want a new AKS branch for that exact upstream patch version.

1. Verify that the minor version is still fully supported.
2. Create a new AKS release branch from the exact upstream tag for that patch version and push it first.

```bash
git fetch upstream --tags
git switch -c cluster-autoscaler-release-1.35.2-aks cluster-autoscaler-1.35.2
git push -u origin cluster-autoscaler-release-1.35.2-aks
```

3. Create a separate working branch from `cluster-autoscaler-release-1.35.2-aks`.

```bash
git switch -c <topic-branch> cluster-autoscaler-release-1.35.2-aks
```

4. Replay the AKS patch queue on the working branch in order:
   - main AKS fork delta commit
   - additional AKS fork commits already approved for that line
  - any new small AKS-only fixes approved for this patch version line
5. If any cherry-pick needs manual conflict resolution, document every resolved conflict in the PR description or in the resolving commit, including the affected files and rationale.
6. Re-evaluate `master-azure`-only candidates only if they are still needed and still compatible.
7. Run validation on the working branch.
8. Open a PR from the working branch into `cluster-autoscaler-release-1.35.2-aks`.
9. Tag the PR commit with `cluster-autoscaler-release-1.35.2-aks-candidate`.
10. Build an image from that candidate tag.
11. After the PR lands and the image from that candidate tag is fully built, add the official tag `cluster-autoscaler-release-1.35.2-aks`.

Do not create a new patch version release by merging forward the previous AKS branch. Rebuild from the upstream tag for that patch version and replay the AKS stack.

### New Revision On An Existing Release Branch

Use this when the upstream patch version base does not change and you need a small AKS-only change on top of an existing AKS release branch.

1. Verify that the release line is still fully supported.
2. Create a working branch from the existing AKS release branch.

```bash
git switch -c <topic-branch> cluster-autoscaler-release-1.35.0-aks
```

3. Cherry-pick or commit the small change on the working branch.
4. Keep the working branch linear and preserve provenance.

```bash
git cherry-pick -x <sha>
```

5. If the change needs manual conflict resolution or adaptation, document every resolved conflict in the PR description or in the resolving commit, including the affected files and rationale.
6. Run validation on the working branch.
7. Open a PR from the working branch into `cluster-autoscaler-release-1.35.0-aks`.
8. Tag the PR commit with the next candidate tag, for example `cluster-autoscaler-release-1.35.0-aks-1-candidate`.
9. Build an image from that candidate tag.
10. After the PR lands and the image from that candidate tag is fully built, add the matching official tag, for example `cluster-autoscaler-release-1.35.0-aks-1`.

Use a revision only for a small AKS-only respin on top of the same upstream patch version base. If the upstream patch version changes, make a new branch for that patch version instead.

## Using master-azure Safely

`master-azure` is not a release source. It is only a place to discover Azure-only commits that may need to be backported into supported AKS release branches.

Build the candidate list from the live branch state:

```bash
git log --no-merges upstream/master..master-azure --oneline
```

Then classify each commit:

- release-noise or metadata-only
- already present on the target AKS branch
- still needed and compatible with supported release branches
- incompatible with older release branches and therefore skip or adapt

Use the newest supported AKS branch first, then work backward only across still-supported branches.

For each candidate commit:

1. Check whether it depends on other `master-azure`-only commits.
2. Check whether it changes APIs, dependencies, or toolchains that differ across `1.33`, `1.34`, and `1.35`.
3. Probe compatibility with a dry-run cherry-pick.

```bash
git cherry-pick -x --no-commit <sha>
```

4. If the cherry-pick does not apply cleanly or requires unsupported APIs on the target release line, abort and either adapt the change or skip it.
5. If you adapt the change or resolve conflicts manually, document every resolved conflict in the PR description or in the resolving commit.
6. Only backport the commit to branches that are still fully supported at execution time.

If `1.33` has already moved into LTS-only or platform support, do not add new fork commits to `1.33` even if similar commits are still being applied to `1.34` and `1.35`.

## Backward Compatibility Review

When evaluating a `master-azure`-only commit for supported release branches, check these points:

- Does the target branch still carry the same Azure SDK or module layout?
- Does the change depend on APIs that were introduced after the target upstream release?
- Does the change alter release tooling or builder images in a way that older branches cannot absorb cleanly?
- Does the change affect runtime behavior, only comments, or only packaging metadata?
- Is the target branch still fully supported, or has it already moved into an ineligible support phase?

Prefer the smallest compatible backport over a broad sync.

## Validation

At minimum, run the same main validation entry points used by this repo's CI:

```bash
hack/verify-all.sh -v
```

```bash
cd cluster-autoscaler && go test -v -coverprofile=coverage.txt -covermode=atomic -race $(go list ./... | grep -v vertical-pod-autoscaler/e2e | grep -v /cloudprovider/ && go list ./cloudprovider/azure/...)
```

Do not use `make release-validate` as the AKS fork release gate. That target expects an upstream-style `cluster-autoscaler-x.y.z` tag on `HEAD` and does not understand the candidate and official AKS tag scheme in this workflow.

After the candidate tag is created, build an image from that candidate tag and finalize the official tag after the image is fully built. This skill stops at preparing the Git history, opening the PR, and creating the candidate and official tags in this repo.

When the change affects Azure cloudprovider behavior, run targeted Azure tests when practical.

## Reviewer Handoff

Before requesting review, include a short release manifest in the PR description with:

- release type
- target release branch
- upstream tag
- candidate tag
- official tag
- cherry-picked commits and source SHAs
- every resolved conflict, including affected files and rationale
- any `master-azure`-derived commits and why they are needed
- validation commands run

For the review pass, use the `verify-azure-ca-release` skill so reviewers can check support eligibility, upstream base, PR commit set, tag correctness, conflict documentation, and validation evidence with the same rubric every time. For the main large fork delta commit, also use the `verify-azure-fork-patch-commit` skill.

## Commit Hygiene

- Use `git cherry-pick -x` for every backport.
- Keep release branches linear.
- Do not merge `master-azure` into release branches.
- Document every resolved conflict in the PR description or in the commit that resolved it. Include the affected files, the chosen resolution, and why it is correct for that release line.
- Use `<fork branch>-candidate` for the initial release candidate tag and `<fork branch>` for the initial official tag.
- Use `<fork branch>-N-candidate` for revision candidate tags and `<fork branch>-N` for revision official tags.
- Record for each AKS release:
  - upstream tag
  - main AKS fork delta commit
  - additional AKS fork commits
  - candidate tag name
  - official tag name
  - resolved conflicts and rationale
  - any `master-azure` candidates applied
  - any `master-azure` candidates intentionally skipped and why
