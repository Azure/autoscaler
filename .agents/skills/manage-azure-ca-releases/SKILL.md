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
- Tag the PR commit with a `vX.Y.Z-aks-N-candidate` tag first, build an image from that candidate tag, then add the matching `vX.Y.Z-aks-N` official tag after the image is fully built.
- Do not cut releases from `master-azure`.
- `master-azure` is only a source of candidate Azure-only commits that may need to be backported.
- Any resolved conflicts must be documented in the PR description or in the commit that resolved them.
- Merge release PRs using **squash-merge** (GitHub "Squash and merge"). Do not use a regular merge commit or rebase-merge. Each PR must land as exactly one clean commit on the release branch, making the stack easy to audit and replay onto the next patch version.
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
- Initial candidate tag: `vX.Y.Z-aks-candidate`
- Initial official tag: `vX.Y.Z-aks`
- First revision candidate tag: `vX.Y.Z-aks-1-candidate`
- First revision official tag: `vX.Y.Z-aks-1`
- Later revision candidate tag: `vX.Y.Z-aks-N-candidate` (incrementing N from 1)
- Later revision official tag: `vX.Y.Z-aks-N`

The initial release for a branch carries no revision number. Only respins on top of the same upstream patch version base are numbered, starting at `1`.

Tags are created **after the PR merges**, not before. Apply the candidate tag to the merge commit on the release branch, trigger the image build, then apply the official tag after the image is fully built.

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
7. The following files conflict on every new minor version in a predictable way and can be resolved without stopping for requestor input:

   - `.devcontainer/devcontainer.json` — keep the fork version (name `"Azure CAS Dev"`, docker-outside-of-docker, Azure CLI, skaffold, ko, yq, AKS VS Code extensions, `remoteUser: vscode`), but bump the `go:X.Y` image tag to match the new upstream minor's Go version.
   - `builder/Dockerfile` — keep the fork's MCR base image (`mcr.microsoft.com/oss/go/microsoft/golang:X.Y.Z`), bumping the version to match upstream. Do not revert to the plain `golang:` base image.

   Document these as standard resolutions in the PR description.

8. If any other cherry-pick produces a conflict, **stop and present the conflict to the requestor** before resolving it. Show:
   - the file and the two competing changes
   - what each side is doing and why they conflict
   - the available resolution options with a recommended option and rationale
   Wait for the requestor to confirm the resolution before proceeding. When applying the agreed resolution, **always replace the entire conflict block in a single operation** — from the `<<<<<<< HEAD` line through the `>>>>>>> <sha>` line inclusive. Never replace only the opening or closing marker alone; partial replacements leave orphaned content that compiles incorrectly. Document the agreed resolution in the PR description or in the resolving commit, including the affected files and rationale.
9. Evaluate whether any `master-azure`-only compatibility commits must also be applied on the working branch.
10. Run validation on the working branch.
11. Open a PR from the working branch into `cluster-autoscaler-release-1.36.0-aks`.
12. After the PR merges, apply the candidate tag to the merge commit on the release branch:
    ```bash
    git tag v1.36.0-aks-candidate <merge-sha>
    git push origin v1.36.0-aks-candidate
    ```
13. Build an image from that candidate tag.
14. After the image is fully built, add the official tag:
    ```bash
    git tag v1.36.0-aks <merge-sha>
    git push origin v1.36.0-aks
    ```

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
5. If any cherry-pick produces a conflict, **stop and present the conflict to the requestor** before resolving it. Show:
   - the file and the two competing changes
   - what each side is doing and why they conflict
   - the available resolution options with a recommended option and rationale
   Wait for the requestor to confirm the resolution before proceeding. When applying the agreed resolution, **always replace the entire conflict block in a single operation** — from the `<<<<<<< HEAD` line through the `>>>>>>> <sha>` line inclusive. Never replace only the opening or closing marker alone; partial replacements leave orphaned content that compiles incorrectly. Document the agreed resolution in the PR description or in the resolving commit, including the affected files and rationale.
6. Re-evaluate `master-azure`-only candidates only if they are still needed and still compatible.
7. Run validation on the working branch.
8. Open a PR from the working branch into `cluster-autoscaler-release-1.35.2-aks`.
9. After the PR merges, apply the candidate tag to the merge commit on the release branch:
    ```bash
    git tag v1.35.2-aks-candidate <merge-sha>
    git push origin v1.35.2-aks-candidate
    ```
10. Build an image from that candidate tag.
11. After the image is fully built, add the official tag:
    ```bash
    git tag v1.35.2-aks <merge-sha>
    git push origin v1.35.2-aks
    ```

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

5. If the cherry-pick produces a conflict, **stop and present the conflict to the requestor** before resolving it. Show:
   - the file and the two competing changes
   - what each side is doing and why they conflict
   - the available resolution options with a recommended option and rationale
   Wait for the requestor to confirm the resolution before proceeding. When applying the agreed resolution, **always replace the entire conflict block in a single operation** — from the `<<<<<<< HEAD` line through the `>>>>>>> <sha>` line inclusive. Never replace only the opening or closing marker alone; partial replacements leave orphaned content that compiles incorrectly. Document the agreed resolution in the PR description or in the resolving commit, including the affected files and rationale.
6. Run validation on the working branch.
7. Open a PR from the working branch into `cluster-autoscaler-release-1.35.0-aks`.
8. After the PR merges, apply the next candidate tag to the merge commit on the release branch (revisions start at `1`):
    ```bash
    git tag v1.35.0-aks-1-candidate <merge-sha>
    git push origin v1.35.0-aks-1-candidate
    ```
9. Build an image from that candidate tag.
10. After the image is fully built, add the matching official tag:
    ```bash
    git tag v1.35.0-aks-1 <merge-sha>
    git push origin v1.35.0-aks-1
    ```

Use a revision only for a small AKS-only respin on top of the same upstream patch version base. If the upstream patch version changes, make a new branch for that patch version instead.

## After Opening the PR

After opening any release PR, add two standard comments:

### Comment 1 — Reviewer guide

Post a comment that gives reviewers the specific checklist for this PR. Include:

- A link to the `verify-azure-ca-release` skill as the review framework
- The support eligibility check and EOL date for the target minor version
- The exact commands to verify the upstream base, PR commit set, and tag state
- A summary of each resolved conflict with the competing changes and the approved resolution
- The validation results from the PR description

### Comment 2 — Next steps after merge

Post a comment with the exact commands to run after the PR merges:

1. Fetch the updated release branch and capture the merge SHA
2. Apply the candidate tag to the merge commit and push it
3. Trigger the image build from the candidate tag
4. Apply the official tag after the image build confirms

Use ready-to-run shell commands with the exact tag names for this release. For example:

```bash
git fetch origin cluster-autoscaler-release-1.35.2-aks
MERGE_SHA=$(git rev-parse origin/cluster-autoscaler-release-1.35.2-aks)
git tag v1.35.2-aks-candidate $MERGE_SHA
git push origin v1.35.2-aks-candidate
# after image build confirms:
git tag v1.35.2-aks $MERGE_SHA
git push origin v1.35.2-aks
```

## Using master-azure Safely

`master-azure` is not a release source. It is only a place to discover Azure-only commits that may need to be backported into supported AKS release branches.

### Finding Unbackported Fork Commits

The reliable way to surface missing backports is to compare fork-specific commits on `master-azure` against fork-specific commits on each release branch.

**Step 1 — Get fork-specific commits on `master-azure`:**

```bash
git log --no-merges upstream/master..origin/master-azure --oneline
```

**Step 2 — Get fork-specific commits on the target release branch:**

```bash
# Replace the upstream tag and branch name for the target release line
git log --no-merges cluster-autoscaler-1.33.4..origin/cluster-autoscaler-release-1.33.4-aks --oneline
```

**Step 3 — Identify what is missing.**

Do not compare by SHA or by patch content. Cherry-picks to release branches often require conflict resolution, which changes the diff and makes patch-id matching unreliable. Instead, compare by commit subject, stripping trailing PR number references (e.g., `(#48)`, `(#61)`) before matching, since the same fix cherry-picked to a different branch gets a new PR number.

Any commit subject that appears in the `master-azure` fork-specific list but has no clear subject match in the release branch fork-specific list is a candidate for backport review.

**Note on non-squash-merged PRs:** If a PR was merged without squash, the individual commit messages on `master-azure` may be meaningless (e.g., "superseded operation", "adding comment"). When you encounter unrecognized subjects, trace them back to their merge commit on `master-azure` to understand what the PR was:

```bash
git log --merges --oneline --ancestry-path <sha>^..origin/master-azure | head -5
```

Or look up the merge commit directly:

```bash
git log --merges --oneline upstream/master..origin/master-azure
```

Then classify each unbackported commit:

- release-noise or metadata-only
- already present on the target AKS branch under a different message
- still needed and compatible with supported release branches
- incompatible with older release branches and therefore skip or adapt

Use the newest supported AKS branch first, then work backward only across still-supported branches.

For each candidate commit or PR:

1. Check whether it depends on other `master-azure`-only commits.
2. Check whether it changes APIs, dependencies, or toolchains that differ across `1.33`, `1.34`, and `1.35`.
3. If the PR on `master-azure` was not squash-merged, the individual commit messages may be meaningless. Cherry-pick the squash commit from the release branch instead (if already backported), or cherry-pick the merge commit with `-m 1` as a one-time landing:

```bash
git cherry-pick -x -m 1 <merge-commit-sha>
```

4. If the PR was squash-merged, cherry-pick that single squash commit directly:

```bash
git cherry-pick -x <sha>
```

5. Probe compatibility with a dry-run cherry-pick before committing:

```bash
git cherry-pick -x --no-commit <sha>
```

6. If the cherry-pick does not apply cleanly or requires unsupported APIs on the target release line, abort and either adapt the change or skip it.
7. If the cherry-pick produces a conflict, **stop and present the conflict to the requestor** before resolving it. Show the competing changes, the available resolution options, and a recommendation. Wait for confirmation before proceeding. When applying the agreed resolution, **always replace the entire conflict block in a single operation** — from the `<<<<<<< HEAD` line through the `>>>>>>> <sha>` line inclusive. Never replace only the opening or closing marker alone; partial replacements leave orphaned content that compiles incorrectly. Document the agreed resolution in the PR description or in the resolving commit.
8. Only backport the commit to branches that are still fully supported at execution time.

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
- Use `vX.Y.Z-aks-1-candidate` for the initial release candidate tag and `vX.Y.Z-aks-1` for the initial official tag.
- Use `vX.Y.Z-aks-N-candidate` for later revision candidate tags and `vX.Y.Z-aks-N` for later revision official tags.
- Record for each AKS release:
  - upstream tag
  - main AKS fork delta commit
  - additional AKS fork commits
  - candidate tag name
  - official tag name
  - resolved conflicts and rationale
  - any `master-azure` candidates applied
  - any `master-azure` candidates intentionally skipped and why
