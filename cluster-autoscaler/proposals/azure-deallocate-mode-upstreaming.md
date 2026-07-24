# Upstreaming Azure Deallocate Mode with Suspended Nodes

## Summary

Cluster Autoscaler 1.36 introduced generic handling for Nodes that carry a `Suspended=True` condition. That gives us a common way to describe an intentionally inactive node, but it does not make Azure deallocate mode upstreamable on its own.

The remaining incompatibility is target-size accounting.

Upstream suspended-node logic assumes `NodeGroup.TargetSize()` already includes suspended nodes. Azure deallocate mode uses `TargetSize()` as active desired capacity, so it subtracts deallocated and deallocating VMs and then carries Azure-specific fixes in `clusterstate` and `core`.

Recommendation:

- Reuse `Suspended=True` as the generic node-side signal for deallocated Azure nodes.
- Keep Azure's resume-before-create behavior inside the Azure provider.
- Add one small generic cloudprovider contract that tells core whether suspended nodes are included in `TargetSize()`.

With that contract, Azure can fit into the upstream suspended-node model without keeping Azure-only logic in `clusterstate` and `core`.

## Current upstream model

- `clusterstate` puts `Suspended=True` nodes in a dedicated `Readiness.Suspended` bucket.
- Suspended nodes count toward node group health.
- Upcoming-node math currently assumes suspended nodes are already part of `CurrentTarget`:

  `CurrentTarget - (Ready + Unready + Suspended + LongUnregistered)`

- GCE fits this model because it reports MIG target as `TargetSize + TargetSuspendedSize`.

## Current Azure deallocate model in this branch

Azure deallocate mode does three separate things today.

### 1. Actuation

- Scale down deallocates VMSS instances instead of deleting them.
- Scale up tries to start deallocated instances before creating new VMs.

### 2. Provider accounting

- `getScaleSetSize()` returns VMSS capacity minus deallocated and deallocating instances.
- That means `TargetSize()` is the desired count of active nodes, not the total VMSS member count.

### 3. Core and fork shims

- `clusterstate` treats shutdown and unreachable nodes as `Deallocated`.
- `GetUpcomingNodes()` has Azure-only math that does not subtract deallocated nodes from target.
- `IsNodeGroupHealthyDeallocate()` bypasses the normal health check.
- `cleanUpTaintsFromDeallocatedNodes()` removes `ToBeDeleted` taints from nodes that were deallocated and later reused.
- `processNodeGroupDeallocate()` skips already deallocated nodes during scale-down eligibility.

The important point is that most of this forked logic exists only because Azure currently has no generic way to say "this node is intentionally inactive, and my target size does not count it."

## Why adding `Suspended=True` is not enough

If Azure only starts adding a `Suspended=True` condition to deallocated Nodes, two things improve immediately:

- health logic becomes much closer to upstream behavior
- the signal becomes generic instead of Azure-specific taint inference

But the target-size mismatch remains.

### Case A: Azure keeps its current active-target semantics

If `TargetSize()` continues to exclude deallocated nodes, core must not subtract suspended nodes from `CurrentTarget`. Otherwise upcoming-node math undercounts or goes negative.

### Case B: Azure switches to GCE-style total-target semantics

If `TargetSize()` starts including suspended nodes, current upstream clusterstate math works, but CA-driven deallocate mode becomes awkward:

- deallocating a node would no longer reduce `TargetSize()`
- `IncreaseSize(delta)` becomes ambiguous when the provider may only resume suspended VMs rather than increase total VMSS capacity
- `TargetSize()` would stop representing the active capacity that Azure deallocate mode is trying to manage

This is why GCE is a useful reference for suspended-node accounting, but not a complete model for Azure deallocate mode. GCE reports suspended nodes as part of provider target size. Azure deallocate uses suspension as the result of CA scale-down itself.

## Proposed design

### 1. Use `Suspended=True` as the generic node-side signal

A deallocated Azure node should surface to core as a Node with `Suspended=True`.

That signal can be produced in one of two ways:

- preferred from an ownership standpoint: a provider-specific controller or AKS-owned component updates the Node condition
- acceptable in-tree alternative: Azure CA updates `node.status.conditions` itself

The design below assumes the node eventually carries `Suspended=True`, regardless of who writes it.

### 2. Add a small optional nodegroup contract for suspended-node accounting

Add an optional `cloudprovider` interface along the lines of:

```go
type SuspendedNodeTargetAccounting interface {
	SuspendedNodesIncludedInTargetSize() bool
}
```

Semantics:

- `true`: `TargetSize()` already includes suspended nodes. Existing upstream behavior. This matches GCE.
- `false`: `TargetSize()` excludes suspended nodes. Core must not subtract suspended nodes from `CurrentTarget` when calculating upcoming nodes. This matches Azure deallocate mode.

Default behavior should be `true` to preserve existing providers.

Core changes would then be:

- keep suspended nodes counted as healthy
- keep suspended nodes separate from normal unready nodes
- parameterize upcoming-node calculation based on the optional interface instead of hardcoding the GCE-style assumption

In practice that means:

- providers with `true` keep using:

  `CurrentTarget - (Ready + Unready + Suspended + LongUnregistered)`

- providers with `false` use:

  `CurrentTarget - (Ready + Unready + LongUnregistered)`

This preserves current `NotStarted` handling.

### 3. Keep suspend and resume actuation provider-specific

Azure provider should continue to own:

- deallocate-on-scale-down behavior
- start-before-create behavior on scale-up
- mapping VMSS power state transitions into provider-side instance state and cache behavior

Core does not need Azure-specific `InstanceDeallocated` or `InstanceDeallocating` semantics in order to reason about cluster health. The cross-provider contract should be:

- node-side inactivity is represented as `Suspended=True`
- target-size accounting behavior is declared explicitly by the nodegroup

### 4. Make taint lifecycle explicit

One Azure-specific operational detail remains even after moving to `Suspended=True`: resumed nodes must not retain `ToBeDeleted` taints.

That can be handled in either place:

- Azure clears the taint when deallocation completes or when resume starts
- core gains a generic rule that suspended nodes reused for capacity must not remain marked for deletion

I would prefer Azure to own this cleanup rather than keep a permanent Azure-specific hook in `core`.

## Expected simplification in this branch

If Azure deallocated nodes are represented as suspended nodes and target-size accounting is parameterized, the following fork-specific code should disappear or shrink substantially:

- `clusterstate/deallocate.go`
- the deallocate branch in `clusterstate.updateReadinessStats()`
- the Azure-specific override in `ClusterStateRegistry.GetUpcomingNodes()`
- `core/deallocate.go`
- `core/scaledown/eligibility/deallocate.go`

The `ScaleDownPolicy() == Deallocate` API can remain provider-specific because it controls actuation, not generic cluster reasoning.

## Azure implementation work

### Azure provider needs a real deallocate-mode detection path

Today, Azure does not infer deallocate mode from live VMSS data.

- `NodeGroupSpec` carries `ScaleDownPolicy`.
- `NewScaleSet()` copies that field into `ScaleSet.scaleDownPolicy`.
- autodiscovered VMSS nodegroups are built from cached VMSS objects, but `getFilteredScaleSets()` does not populate any deallocate-mode field from Azure metadata.

That is fine for the current fork, where deallocate mode is effectively configuration attached to the nodegroup spec. It is not a good fit for an upstream design where Azure should behave correctly from provider state alone.

The Azure provider will need a helper that determines, for a specific VMSS, whether the provider should treat it as delete mode or deallocate mode. Conceptually it should look like:

```go
func detectScaleSetDeallocateMode(vmss *armcompute.VirtualMachineScaleSet) (bool, error)
```

or, if we want to preserve a provider-local enum:

```go
func detectScaleSetScaleDownMode(vmss *armcompute.VirtualMachineScaleSet) (ScaleDownMode, error)
```

The important point is not the exact signature. The important point is that Azure should derive this from live provider metadata, not only from the CA nodegroup spec.

This detection is needed in two places.

#### 1. Nodegroup construction and startup

When Azure builds a `ScaleSet`, it needs to know whether that VMSS is resumable/deallocating or ordinary delete-on-scale-down.

That applies to:

- explicitly configured nodegroups after the initial `forceRefresh()` has loaded VMSS objects
- autodiscovered nodegroups created from cached VMSS list results

If the provider keeps any user-facing configuration knob, live detection should still be the source of truth. A mismatch between config and VMSS metadata should be treated as an error or at least logged loudly.

#### 2. Refresh and cache invalidation

This is not only a startup concern.

The detected mode influences several behaviors that are cached or interpreted over time:

- `TargetSize()` math in deallocate mode subtracts deallocated and deallocating instances
- `IncreaseSize()` changes from create-only to resume-before-create
- `DeleteNodes()` and `ForceDeleteNodes()` choose delete versus deallocate actuation
- instance-status interpretation changes in the VMSS instance cache, especially around failed provisioning and deallocated power states

Because of that, if the detected mode for a VMSS changes, Azure should invalidate at least:

- the instance cache for that scale set
- any cached size derived from deallocate-aware accounting
- any cached per-nodegroup behavior derived from the old mode

In practice, the cleanest shape is:

- treat the mode as VMSS metadata associated with the cached VMSS object
- re-evaluate it whenever the cached VMSS object is refreshed or replaced
- if the mode changed, update the `ScaleSet` object and invalidate the relevant caches

If the needed Azure field is present in the VMSS list payload, the helper can run directly against the cached VMSS objects already stored in `azureCache`.

If the list payload is insufficient and only a per-VMSS GET exposes the needed field, Azure should do a lazy GET during nodegroup registration or refresh and persist the result alongside the cached VMSS entry so it does not become a per-loop API tax.

Either way, this wants to live in the Azure provider's existing VMSS discovery and cache-refresh flow, not as a one-off special case in core.

### Option 1: external writer for `Suspended=True`

Use a provider-specific controller or AKS-owned component to write the Node condition based on VMSS power state.

Pros:

- keeps Kubernetes node-status mutation out of CA cloudprovider code
- cleaner ownership boundary between provider logic and Kubernetes object updates

Cons:

- requires another component or AKS integration point
- upstream CA behavior depends on an external actor being present

### Option 2: Azure CA writes `Suspended=True`

Teach Azure provider code to update `node.status.conditions` directly when it deallocates or resumes a node.

This would require:

- plumbing a Kubernetes client or dedicated node-status writer into Azure provider code
- deciding where node lookup and cache lives
- adding `nodes/status` RBAC

Today the Azure provider does not have that wiring:

- `RegisterCloudProvider()` receives an informer factory, but `BuildAzure()` ignores it
- the example Azure manifest grants `update` on `nodes`, but not `nodes/status`

Pros:

- self-contained behavior inside CA
- easier to test in-tree

Cons:

- increases CA and provider coupling to Kubernetes object mutation
- expands RBAC and client plumbing in Azure provider

## Recommended path

1. Upstream a small generic contract for suspended-node target accounting.
2. Convert core upcoming-node math to use that contract instead of Azure-specific branches.
3. Use `Suspended=True` as the generic representation of deallocated Azure nodes.
4. Keep Azure resume-before-create behavior provider-specific.
5. Prefer Azure-owned taint cleanup over keeping Azure-specific core cleanup hooks.
6. Decide separately whether the `Suspended` condition is written by Azure CA itself or by an external controller.

This gives Azure a path to upstream deallocate mode with minimal new generic surface area:

- one generic node signal: `Suspended=True`
- one generic nodegroup contract: whether suspended nodes are included in `TargetSize()`

Everything else stays provider-specific.

## Open questions

- Should the default for `SuspendedNodesIncludedInTargetSize()` be implicit `true`, or should providers that surface suspended nodes be required to implement the interface explicitly?
- Should core add any generic scale-down eligibility rule for suspended nodes, or is provider-owned taint cleanup enough?
- If Azure CA writes the Node condition itself, do we want that responsibility in-tree, or is that better left to an external controller or AKS integration?
- Do we want to keep generic `InstanceDeallocated` and `InstanceDeallocating` states at all, or should Azure treat those as provider-internal details once `Suspended=True` exists?

## Relevant code paths

- `clusterstate/clusterstate.go`
- `clusterstate/deallocate.go`
- `core/deallocate.go`
- `core/scaledown/eligibility/deallocate.go`
- `cloudprovider/azure/azure_scale_set.go`
- `cloudprovider/azure/azure_scale_set_instance_cache.go`
- `cloudprovider/azure/azure_cloud_provider.go`
- `cloudprovider/azure/examples/cluster-autoscaler-vmss.yaml`
- `cloudprovider/gce/autoscaling_gce_client.go`
- `cloudprovider/gce/mig_info_provider.go`

## Non-goal

This proposal does not try to make CA itself own a generic suspend and resume lifecycle across providers. It only tries to make core cluster reasoning aware of intentionally inactive nodes, while letting each provider decide how those nodes are created, suspended, resumed, or removed.
