# Azure Fork Commit Notes

## Scope

This document tracks what the original Azure fork commit contained and captures the current test organization approach for deallocate-mode behavior.

## Original Fork Commit

- Commit: `64cc7b4f750f091152d56138fab88b3a5ed63b3e`
- Title: `test: add ForceDeleteNodes deallocate-mode routing test`
- File(s) changed:
  - `cluster-autoscaler/cloudprovider/azure/azure_scale_set_test.go`

## Change Breakdown (Categorized)

### 1) Deallocate routing test coverage

- Added unit coverage validating `ForceDeleteNodes` routing behavior by scale-down policy:
  - Deallocate mode routes to `BeginDeallocate`
  - Delete mode routes to `BeginDeleteInstances`

### 2) Non-goals in the original commit

- No production/provider behavior changes
- No API surface changes
- No e2e changes

## Ongoing Test Organization Approach

For Azure deallocate-specific behavior, isolate tests into deallocate-focused test files whenever practical.

Preferred pattern:

- Keep generic Azure behavior tests in broad files (for example, `azure_scale_set_test.go`, `azure_scale_set_instance_cache_test.go`)
- Move deallocate-specific scenarios into dedicated files (for example, `*_deallocate_test.go`)

Why:

- Simplifies future deforking by minimizing Azure-fork-specific edits in mixed test files
- Makes intent explicit for reviewers (`deallocate` behavior is easy to locate)
- Reduces maintenance friction when upstream test structure evolves

## Current Isolation Examples

- `azure_scale_set_deallocate_test.go`
- `azure_scale_set_instance_cache_deallocate_test.go`
