# Azure Fork Commit Notes

## Scope

This document records the Azure fork baseline that was previously maintained internally and later moved to GitHub. The goal is to make the fork surface area explicit, improve release visibility, reuse AKS upstream-managed release infrastructure, and make future deforking easier.

## Fork Baseline Commit

- Commit: `0743c9e47da3ac7df6232b650262e3b178cb909a`
- Title: `cherrypick: fork differences from 1.34 for 1.35`

This commit is the baseline reference point for the forked Azure-specific changes discussed here.

## Why The Fork Was Moved To GitHub

The fork was moved from an internal-only location to GitHub in order to:

- add better visibility into Azure-managed releases
- utilize existing AKS/upstream infrastructure to create managed releases
- support the ongoing effort to fully defork Azure-specific changes over time

## Primary Categories Of Fork Changes

The main fork-specific features introduced by the baseline commit fall into two primary categories.

### 1) Dynamic config support

This category adds support for dynamic configuration and the supporting parsing/fetching flow around it.

Representative files from the baseline commit:

- `cluster-autoscaler/config/dynamic/config.go`
- `cluster-autoscaler/config/dynamic/config_fetcher.go`
- `cluster-autoscaler/config/dynamic/node_group_spec.go`
- `cluster-autoscaler/config/dynamic/config_test.go`
- `cluster-autoscaler/config/dynamic/node_group_spec_test.go`
- `cluster-autoscaler/config/flags/flags.go`
- `cluster-autoscaler/main.go`
- `cluster-autoscaler/core/static_autoscaler.go`

### 2) Deallocate scale-down policy support for VMSS

This category adds Azure-specific support for a deallocate-based VMSS scale-down policy, including provider logic, clusterstate handling, core eligibility flow, taints, and tests.

Representative files from the baseline commit:

- `cluster-autoscaler/cloudprovider/azure/deallocate/cloud_provider_deallocate.go`
- `cluster-autoscaler/cloudprovider/azure/azure_scale_set.go`
- `cluster-autoscaler/cloudprovider/azure/azure_scale_set_deallocate_test.go`
- `cluster-autoscaler/cloudprovider/azure/azure_scale_set_instance_cache.go`
- `cluster-autoscaler/cloudprovider/azure/azure_scale_set_instance_cache_test.go`
- `cluster-autoscaler/cloudprovider/azure/azure_util.go`
- `cluster-autoscaler/cloudprovider/azure/azure_vms_pool.go`
- `cluster-autoscaler/clusterstate/deallocate.go`
- `cluster-autoscaler/clusterstate/clusterstate.go`
- `cluster-autoscaler/core/deallocate.go`
- `cluster-autoscaler/core/scaledown/eligibility/deallocate.go`
- `cluster-autoscaler/utils/taints/deallocate.go`
- `cluster-autoscaler/cloudprovider/cloud_provider.go`

## Secondary Categories Of Fork Changes

The same baseline commit also included supporting changes that are not the primary product features, but are part of the fork footprint.

### 3) Supporting interfaces and wiring

These changes extend shared interfaces or generic orchestration paths to make the Azure-specific features possible.

Examples:

- `cluster-autoscaler/cloudprovider/cloud_provider.go`
- `cluster-autoscaler/cloudprovider/test/test_cloud_provider.go`
- `cluster-autoscaler/clusterstate/clusterstate.go`
- `cluster-autoscaler/main.go`
- `cluster-autoscaler/core/static_autoscaler.go`

### 4) Release, CI, and packaging support

These changes support the operational side of maintaining the forked distribution.

Examples:

- `.azuredevops/pull_request_template.md`
- `.pipelines/cluster-autoscaler.yaml`
- `builder/Dockerfile`

## Minimizing Mixed Fork / Non-Fork Files

As much as possible, fork-specific logic should live in separate files from upstream implementation.

The goal is to minimize the number of files that contain both:

- upstream behavior that should remain after deforking
- Azure-fork-specific behavior that may eventually be removed or upstreamed

Why this matters:

- it reduces the cost of removing fork-only behavior later
- it makes code review clearer by keeping Azure-only logic easy to spot
- it lowers the risk of future upstream rebases creating unnecessary conflicts
- it makes eventual upstreaming or deletion more mechanical and less error-prone

Preferred approach:

- when adding Azure-fork-specific behavior, prefer a dedicated file over extending a mixed file
- only modify an existing upstream file when there is no practical abstraction boundary
- when mixed files are unavoidable, keep the fork-specific surface narrowly scoped and easy to identify

## Test File Isolation Guidance

The file-isolation rule also applies to tests.

For scale-down-policy-specific unit tests:

- add generic or delete-policy behavior tests to `<main test file>.go`
- add deallocate-policy-specific behavior tests to `<deallocate test file>.go`

Examples:

- generic Azure scale-set tests belong in files such as `azure_scale_set_test.go`
- deallocate-specific Azure scale-set tests belong in files such as `azure_scale_set_deallocate_test.go`
- generic instance-cache tests belong in files such as `azure_scale_set_instance_cache_test.go`
- deallocate-specific instance-cache tests belong in files such as `azure_scale_set_instance_cache_deallocate_test.go`

This organization makes policy-specific behavior easier to find and easier to remove once the fork-specific feature is no longer needed or has been fully upstreamed.
