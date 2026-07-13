---
name: create-azure-release-draft
description: 'Create a draft GitHub release on Azure/autoscaler after an AKS release PR merges and the official tag is applied. Use when: creating the draft release notes for a new AKS patch version, new minor version, or revision; generating the body from the upstream diff and AKS cherry-pick list; publishing the draft to GitHub for review before it goes live.'
---

# Create Azure CA Draft Release

## When to Use

Run this skill **after**:
1. The release PR has merged into the AKS release branch.
2. The candidate tag has been applied and the image build has confirmed.
3. The official tag (`vX.Y.Z-aks` or `vX.Y.Z-aks-N`) has been applied to the merge commit.

Do not create the draft release before the official tag exists — the tag is required for `gh release create`.

## Inputs Needed

Gather these before generating the release body:

- Official tag name (e.g. `v1.33.5-aks`)
- Release type: new patch version, new minor version, or revision
- Upstream base tag (e.g. `cluster-autoscaler-1.33.5`)
- Previous upstream tag for this minor version (e.g. `cluster-autoscaler-1.33.4`)
- The merged PR number and URL
- List of AKS cherry-picks with descriptions (from the PR description)
- Resolved conflicts and their rationale (from the PR description)
- Any known gaps or deferred follow-ups

## Generating the Release Body

### 1. Gather upstream changes

Diff the previous upstream tag to the new one, focusing on Azure and core changes:

```bash
git log --no-merges --oneline <prev-tag>..<new-tag> | grep -iE "azure|fix|feat|vmss|scale|delete|cache|atomic|template|arm64|dra|csi"
```

Group the results into:
- **Azure cloud provider fixes** (files under `cloudprovider/azure/`)
- **Core / scale-down / scheduler fixes**
- **Dependency updates** (k8s deps version bump)

### 2. Summarize AKS cherry-picks

For each cherry-pick in the PR description, write a one-sentence summary of what it fixes or adds and why it matters to AKS customers.

### 3. Note known gaps and follow-ups

List any deferred work, such as:
- Features introduced upstream that the fork has not yet wired up
- Follow-up revisions planned (`vX.Y.Z-aks-1`, etc.)
- Test coverage gaps

## Release Body Format

Use this structure for the release body:

```markdown
## What's new

### Upstream changes included in this release

**Azure cloud provider**

- **fix: <short title>** — <one-sentence description of the bug and fix>
- ...

**Core / scale-down / scheduler**

- **fix: <short title>** — <one-sentence description>
- ...

**Dependency updates**
- Kubernetes deps → vX.Y.Z

---

### AKS-specific changes

- **<commit title>** ([PR #N](<url>)) — <what it fixes and why it matters for AKS>
- ...

---

### Known gaps and follow-ups

- <item> — <status and planned revision if applicable>

---

Full release PR: [#N](<PR URL>)
Upstream base: [`<upstream-tag>`](<kubernetes/autoscaler release URL>)
```

Keep each bullet concise — one sentence for the what, one for the why. Avoid implementation details unless they are needed to understand the customer impact.

## Creating the Draft Release

```bash
gh release create <official-tag> \
  --repo Azure/autoscaler \
  --title "<official-tag>" \
  --notes-file <body-file> \
  --draft \
  --target <release-branch>
```

Example:

```bash
gh release create v1.33.5-aks \
  --repo Azure/autoscaler \
  --title "v1.33.5-aks" \
  --notes-file /tmp/release-body.md \
  --draft \
  --target cluster-autoscaler-release-1.33.5-aks
```

After running the command, open the draft URL that is returned and review the formatting before sharing with the team.

## Multiple Releases in One Session

If releasing multiple minor versions at the same time (e.g. 1.33.5-aks, 1.34.4-aks, 1.35.1-aks), generate one draft per tag. The upstream change sections will differ per minor version; the AKS cherry-pick section can be unified where the commits are shared.

Consider creating a single combined release notes document first (as a local markdown file) to ensure consistency across the three drafts, then generate each draft from the appropriate section.

## After the Draft Is Reviewed

Once the draft is approved:

```bash
gh release edit <official-tag> --repo Azure/autoscaler --draft=false
```

Do not publish the release until the image build for the official tag has fully completed and the tag has been verified to point at the correct merge commit.
