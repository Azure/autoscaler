---
name: verify-azure-fork-patch-commit
description: 'Verify the large Azure Cluster Autoscaler fork patch commit for correctness. Use when: reviewing the main AKS fork delta commit for a new release line, checking a `cherrypick: fork differences ...` commit, auditing resolved merge conflicts, comparing the new fork delta against the previous release line, or confirming that the fork patch commit only contains intended Azure-specific changes.'
---

# Verify Azure CA Fork Patch Commit

## Goal

Review the main AKS fork delta commit as its own artifact.

This is the large commit that usually carries most fork-specific behavior for a new AKS release line, for example:

- `cherrypick: fork differences from 1.32 for 1.33`
- `cherrypick: fork differences from 1.33 for 1.34`
- `cherrypick: fork differences from 1.34 for 1.35`

The review should answer:

- Is this really the intended fork delta for the new release line?
- Does it stay within the expected Azure and fork-owned surfaces?
- Are all non-trivial differences from the previous release line intentional?
- Were any resolved conflicts documented clearly enough for a later maintainer to reconstruct the decision?
- Did the author provide enough validation evidence for a commit of this size?

Return findings in this order:

1. blockers
2. risks or missing evidence
3. concise pass summary

## Reviewer Inputs

Ask the PR author to provide these facts if they are not already present:

- target release branch
- upstream base tag
- candidate tag name
- official tag name
- current fork patch commit SHA
- previous release line fork patch commit SHA used as the comparison baseline
- list of intentionally changed files or feature areas relative to the previous line
- every resolved conflict, including affected files and rationale
- validation commands run and results

If any of these are missing, report that as a review gap.

## Review Workflow

### 1. Identify the Fork Patch Commit

Confirm that the PR has a clearly identifiable main fork patch commit.

Useful commands:

```bash
git log --oneline origin/<release-branch>..HEAD
git show --stat --summary <fork-patch-sha>
```

Expected shape:

- one main large commit carries the fork delta for the new release line
- smaller follow-up commits, if any, are clearly separated and explained

Fail if the fork delta is spread across multiple undocumented commits or mixed with unrelated review noise.

### 2. Compare Against the Previous Release Line

Review the current fork patch commit against the previous line's fork patch commit.

Useful commands:

```bash
git show --stat --summary <previous-fork-patch-sha>
git show --stat --summary <fork-patch-sha>
git diff-tree --no-commit-id --name-status -r <previous-fork-patch-sha>
git diff-tree --no-commit-id --name-status -r <fork-patch-sha>
```

Check that:

- the same major fork-owned areas are present unless there is an explicit reason to add or remove one
- new files or removed files relative to the previous line are explained
- version-specific updates are limited to what the new upstream line requires

Fail if large new surfaces appear without explanation or if expected fork-owned surfaces disappear unexpectedly.

### 3. Verify Touched Areas

For recent fork patch commits, the expected surfaces include:

- `cluster-autoscaler/cloudprovider/azure/**`
- `cluster-autoscaler/config/**`
- `cluster-autoscaler/clusterstate/**`
- `cluster-autoscaler/core/**`
- `cluster-autoscaler/main.go`
- `cluster-autoscaler/go.mod` and `cluster-autoscaler/go.sum`
- fork-owned CI or PR-template files such as `.azuredevops/**` and `.pipelines/**`
- builder or verification adjustments when required by the fork

Unexpected files are not automatically wrong, but they must be justified.

Fail if the commit reaches into unrelated product areas without a clear release-specific reason.

### 4. Verify Conflict Documentation

Any resolved conflict must be documented in the PR description or in the commit that resolved it.

For each resolved conflict, expect:

- affected file or files
- the competing change being reconciled
- the chosen resolution
- why that resolution is correct for this release line
- whether the resolution intentionally changes behavior from the previous line
- confirmation that the resolution was reviewed and approved by the requestor before it was applied

Notes such as `w/basic merge conflict resolution` are not sufficient by themselves. The reviewer should be able to reconstruct the actual decision.

Fail if manual conflict resolution happened but the rationale is undocumented or shows no evidence of requestor review.

### 5. Verify Provenance

The fork patch commit should have a traceable origin.

Check whether the PR description explains:

- which earlier fork patch commit it was derived from
- whether the new commit is a replay, adaptation, or selective carry-forward
- which extra commits were folded into it, if any

If follow-up commits are cherry-picked separately, prefer `git cherry-pick -x` for those commits.

Fail if the reviewer cannot tell how the new fork patch commit was produced.

### 6. Verify Release-Line Compatibility

Check whether the fork patch commit still matches the target upstream release line.

Focus on:

- Azure SDK and dependency changes in `go.mod` and `go.sum`
- flag registration and main wiring changes
- Azure provider interfaces and cloudprovider plumbing
- tests added or adapted for the new line

Fail if the commit appears to carry stale behavior from the previous line that no longer fits the target upstream base.

### 7. Verify Validation Evidence

Expect at least the main validation entry points used in the release workflow:

```bash
hack/verify-all.sh -v
```

```bash
cd cluster-autoscaler && go test -v -coverprofile=coverage.txt -covermode=atomic -race $(go list ./... | grep -v vertical-pod-autoscaler/e2e | grep -v /cloudprovider/ && go list ./cloudprovider/azure/...)
```

For a large fork patch commit, reviewers should prefer stronger evidence than for a tiny revision. If Azure behavior changed materially, ask for targeted Azure validation as well.

Fail if validation is missing, partial, or does not match the size of the fork patch commit.

## Reviewer-Friendly PR Structure

To make this review tractable, ask authors to include a focused fork-patch manifest in the PR body:

```text
Fork patch commit: <sha>
Previous line fork patch commit: <sha>
Target release branch: cluster-autoscaler-release-1.35.2-aks
Upstream tag: cluster-autoscaler-1.35.2
Candidate tag: v1.35.2-aks-candidate
Official tag: v1.35.2-aks
Expected fork-owned areas:
- azure provider
- config/dynamic
- deallocate paths
Resolved conflicts:
- <file>: chose upstream/fork/mixed resolution because ...
Intentional differences from previous line:
- <file or area>: why it changed
Validation:
- hack/verify-all.sh -v
- Azure go test command
```

If this manifest is missing, recommend adding it before full review.

## Review Output

When you finish the verification, summarize with a flat checklist:

- fork patch commit identified: pass or fail
- comparison with previous line: pass or fail
- touched areas are justified: pass or fail
- resolved conflicts documented: pass or fail
- provenance is clear: pass or fail
- release-line compatibility: pass or fail
- validation evidence: pass or fail

Then list blockers first. If there are no blockers, state that explicitly and mention any residual risk or missing evidence.
