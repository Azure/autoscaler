# Design: Tail-Side Error Handling for Azure VMSS Scale Operations

**Date**: March 6, 2026  
**Status**: Draft  
**Author**: Rachel Gregory  
**Related Issues**:
- [Azure/AKS#5589](https://github.com/Azure/AKS/issues/5589) — CAS doesn't backoff deallocate-mode nodepools on failed provisioning
- VM provisioning state handling maintenance burden
- Deallocate mode doesn't handle scale-up timeout
- Can we communicate tail-side errors from async VM provisioning to CAS core?

---

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Background: How Errors Flow Today](#2-background-how-errors-flow-today)
3. [Background: VM Provisioning States and Power States](#3-background-vm-provisioning-states-and-power-states)
4. [Current State: Failure Taxonomy](#4-current-state-failure-taxonomy)
5. [Current State: Gaps in Error Handling](#5-current-state-gaps-in-error-handling)
6. [Design Options](#6-design-options)
7. [Recommended Approach: Enhanced Instance Status Detection (Option C)](#7-recommended-approach-enhanced-instance-status-detection-option-c)
8. [Detailed Design](#8-detailed-design)
9. [Deallocate Mode Specific Fixes](#9-deallocate-mode-specific-fixes)
10. [Open Questions](#10-open-questions)
11. [References](#11-references)
12. [Appendix A: Future Options](#appendix-a-future-options)

---

## 1. Problem Statement

Azure VMSS scaling operations are long-running operations (LROs) that execute in two phases:

1. **Synchronous (head-side)**: The initial `Begin*` API call. Errors here (auth, validation, immediate quota) are returned directly to the autoscaler core and trigger appropriate backoff/failover.

2. **Asynchronous (tail-side)**: The `PollUntilDone` completion. Errors here (allocation failure, CSE timeout, extension provisioning failure) occur in background goroutines and **cannot propagate to the caller**.

Today, tail-side errors are handled indirectly through cache invalidation and periodic instance status polling. This works reasonably well for **delete mode** node groups (where `enableFastDeleteOnFailedProvisioning` triggers cleanup), but has significant gaps for **deallocate mode** and certain timeout scenarios.

**The result**: When tail-side errors occur, the autoscaler may:
- Continue attempting to scale up a failing node group instead of backing off
- Fail to trigger priority expander failover to healthy node groups
- Leave the cluster in a corrupted state (nodes that appear provisioning but are actually failed)
- Take 30–45+ minutes to self-heal through timeout-based remediation

### Scope

This design covers tail-side error handling for **VMSS scale operations** only:
- Scale-up: `BeginCreateOrUpdate` (capacity increase)
- Scale-down (delete): `BeginDeleteInstances`
- Scale-down (deallocate, fork): `BeginDeallocate`
- Scale-up (reallocation, fork): `BeginStart` (starting deallocated VMs)

---

## 2. Background: How Errors Flow Today

### 2.1 Synchronous (Head-Side) Error Path — Works Well

All four VMSS operations share the same sync error pattern — if the initial `Begin*` call fails, the error is returned to the caller and handled appropriately:

```
IncreaseSize(delta)  /  DeleteNodes(nodes)
  → Begin*(ctx, rg, vmss, ...)
  → ERROR returned immediately
  → scaleUpExecutor catches error  /  NodeDeletionBatcher catches error
  → RegisterFailedScaleUp()  /  NodeDeleteResult{ErrorFailedToDelete}
    → backoffNodeGroup()                          ← exponential backoff: 5m → 10m → 20m → 30m
    → metrics + k8s events
  → Priority expander can try next node group on retry  /  Node untainted, retry next iteration
```

This path handles: authentication failures, request validation errors, immediate quota exceeded, throttling (429).

### 2.2 Asynchronous (Tail-Side) Error Paths — Differ by Operation

Each operation type has a different tail-side error flow because the recovery mechanisms and impact differ.

#### 2.2.1 Scale-Up: `BeginCreateOrUpdate` — Indirect Recovery via Instance Status

```
IncreaseSize(delta)
  → BeginCreateOrUpdate succeeds (HTTP 200/201)
  → RegisterScaleUp(nodeGroup, delta)             ← scale-up recorded as SUCCESSFUL
  → curSize proactively updated to newSize
  → go waitForCreateOrUpdateInstances(poller)     ← background goroutine
       │
       └─ PollUntilDone fails (allocation failure, CSE timeout, etc.)
            → klog.Errorf("Failed to update capacity...")    ← only logged
            → invalidateInstanceCache()
            → invalidateLastSizeRefreshWithLock()
            → manager.invalidateCache()
            → [no backoff, no failed scale-up event, no failover signal]
```

**Recovery**: On the next loop, `instanceStatusFromVM()` may detect VMs with `ProvisioningState: Failed` + non-running power state → triggers fast-delete path → `handleInstanceCreationErrors` → backoff. But only if `enableFastDeleteOnFailedProvisioning` is enabled.

**Partial success**: If Azure provisioned 3 of 5 requested VMs before the LRO failed, those 3 VMs exist and are functional. The cache shows `curSize = oldSize + 5` but only 3 VMs actually exist. On next refresh, the real count is discovered.

#### 2.2.2 Scale-Up (Reallocation): `BeginStart` — No Recovery Path (Fork)

```
IncreaseSize(delta) [deallocate mode, deallocated VMs available]
  → setScaleSetSize(newSize, delta)
    → startInstances(instanceIdsToStart)
      → BeginStart succeeds (HTTP 200/202)
      → Cache marks instances as InstanceDeallocating→Running (transitional)
      → go waitForStartInstances(poller)            ← background goroutine
           │
           └─ PollUntilDone fails:
                CASE A — Capacity error (VM can't be started):
                  → VM stays PowerState/deallocated, ProvisioningState: Failed
                  → klog.Errorf(...)
                  → invalidateInstanceCache()
                  → [no backoff, no error signal]
                  → CAS will try to start same VM again next iteration → infinite loop

                CASE B — Extension timeout (VM starts but CSE fails):
                  → VM reaches PowerState/running, ProvisioningState: Failed
                  → klog.Errorf(...)
                  → invalidateInstanceCache()
                  → [no backoff, no error signal]
                  → VM is running but broken; shutdown taint still applied
                  → Node never becomes Ready → stuck until MaxNodeProvisionTime (15 min)

                CASE C — Context deadline exceeded (30 min timeout):
                  → VM state is ambiguous (may be running, may be stopped)
                  → klog.Errorf("WaitForStartInstancesResult failed: context deadline exceeded")
                  → invalidateInstanceCache()
                  → [no backoff, no error signal]
                  → Cluster state potentially corrupted for 45+ min
```

**Key difference from CreateOrUpdate**: The `instanceStatusFromVM()` fast-delete path was designed for **new VMs that failed to create**. It checks `ProvisioningState: Failed` + non-running power state → sets `InstanceCreating + ErrorInfo` → triggers deletion. But for deallocate mode:

- **Case A**: VM is `Failed` + `deallocated` (non-running). The fast-delete path would classify this as `InstanceCreating + ErrorInfo`, triggering `deleteCreatedNodesWithErrors()`. But **deleting a deallocated VM is wrong** in deallocate mode — the whole purpose is to keep VMs for reuse. And even if it triggers backoff, it shouldn't destroy the VM.
- **Case B**: VM is `Failed` + `running`. The fast-delete path sees a running VM and **skips it** (`isRunningVmPowerState` returns true). No error is reported. The VM stays broken until `MaxNodeProvisionTime` elapses.
- **Case C**: VM state is unknown until cache refreshes. Same as Case B if the VM ended up running.

**Net result**: `BeginStart` failures have **no effective recovery path** in the current code.

#### 2.2.3 Scale-Down (Delete): `BeginDeleteInstances` — Self-Correcting

```
DeleteNodes(nodes)
  → DeleteInstances(refs, hasUnregisteredNodes)
    → BeginDeleteInstances succeeds
    → Cache proactively marks instances as InstanceDeleting
    → Cache proactively decrements curSize
    → go waitForDeleteInstances(poller)             ← background goroutine
         │
         └─ PollUntilDone fails:
              → klog.Errorf("PollUntilDone for DeleteInstances failed: %v")
              → invalidateInstanceCache() (if !StrictCacheUpdates)
              → [no explicit error signal to core]
```

**Recovery**: On next loop, `CloudProvider.Refresh()` re-fetches the instance list. The "deleted" VMs reappear (they weren't actually deleted). The node still exists in Kubernetes with the `ToBeDeletedByClusterAutoscaler` taint. Then:
- `fixNodeGroupSize()` detects the mismatch between target size and actual size
- The node may be reconsidered for scale-down in a future iteration
- The taint eventually gets cleaned up or the node is retried for deletion

**This is relatively benign** because:
- The node exists in both Azure and Kubernetes → no phantom state
- The taint prevents pods from scheduling on it → limited impact
- Retry will happen naturally in subsequent iterations

#### 2.2.4 Scale-Down (Deallocate): `BeginDeallocate` — Similar to Delete, with Caveats (Fork)

```
DeleteNodes(nodes) [deallocate mode]
  → deallocateInstances(refs)
    → BeginDeallocate succeeds
    → Cache marks instances as InstanceDeallocating
    → go waitForDeallocateInstances(poller)          ← background goroutine
         │
         └─ PollUntilDone fails:
              → klog.Errorf(...)
              → invalidateInstanceCache()
              → [no explicit error signal]
```

**Recovery**: Similar to delete mode — on next refresh, the VM is still in its pre-deallocation state (running or stopped). The difference is that the VM is supposed to remain in the VMSS, so there's no "missing node" problem. However:

- If the VM was supposed to be deallocated but is still running, it's consuming compute resources
- The `ToBeDeleted` taint may or may not be cleaned up depending on the node's state
- The `cleanUpTaintsFromDeallocatedNodes()` function only removes taints from nodes with shutdown/unreachable taints, which won't be present if deallocation never happened

### 2.3 Recovery Path Summary

| Operation | Tail-Side Error Recovery | Time to Recovery | Backoff? | Self-Correcting? |
|-----------|-------------------------|------------------|----------|------------------|
| **CreateOrUpdate** | Instance status polling → fast-delete path | 5–15 min (if `enableFastDeleteOnFailedProvisioning`) | ⚠️ Indirect | ⚠️ Partially |
| **Start (deallocate)** | **None effective** | 15–45+ min (timeout-based) | ❌ No | ❌ No |
| **DeleteInstances** | Instance reappears → `fixNodeGroupSize` | 1–2 loop iterations | N/A | ✅ Yes |
| **Deallocate** | Instance stays running → retry | 1–2 loop iterations | N/A | ⚠️ Partially |

### 2.3 Indirect Recovery Path (Next Loop Iteration)

Recovery relies on the **next loop's instance status polling**:

```
Next RunOnce() iteration:
  → CloudProvider.Refresh()
    → VMSS instance list re-fetched from Azure
    → instanceStatusFromVM() evaluates each VM:
        If ProvisioningState == "Failed" AND !isRunningPowerState:
          → InstanceCreating + ErrorInfo              ← only if enableFastDeleteOnFailedProvisioning
  → ClusterStateRegistry.UpdateNodes()
    → handleInstanceCreationErrors()
      → buildInstanceToErrorCodeMappings()
        → finds instances with InstanceCreating + ErrorInfo
      → registerFailedScaleUpNoLock()                 ← NOW backoff happens
      → decrease pending scale-up request
  → deleteCreatedNodesWithErrors()
    → nodeGroup.DeleteNodes() or ForceDeleteNodes()   ← cleanup
```

**Time to recovery**: Depends on:
- Instance cache TTL (default ~5 min)
- Whether `enableFastDeleteOnFailedProvisioning` is enabled
- Whether the VM has a non-running power state (required for fast-delete path)
- Worst case: `MaxNodeProvisionTime` (default 15 min) before timeout-based detection

### 2.4 Timeout Interaction: `MaxNodeProvisionTime` vs `asyncContextTimeout`

There are two independent timeout mechanisms that interact poorly, leading to **error masking**:

| Timeout | Default | Where It Runs | What It Does |
|---------|---------|---------------|-------------|
| `MaxNodeProvisionTime` | 15 min (flag `--max-node-provision-time`) | Main loop, in `ClusterStateRegistry` | Expires scale-up requests for nodes that haven't registered in Kubernetes |
| `asyncContextTimeout` | 30 min (hardcoded in `azure_scale_set.go`) | Background goroutine | Context deadline for `PollUntilDone` on the Azure LRO |

#### The Error Masking Problem

When `MaxNodeProvisionTime` is shorter than the time Azure takes to surface a provisioning error, the autoscaler sees a **generic timeout** instead of the real error:

```
Time 0:00  — IncreaseSize(5) → BeginCreateOrUpdate succeeds
             RegisterScaleUp(nodeGroup, 5, time.Now())
             Scale-up request recorded: ExpectedAddTime = now + MaxNodeProvisionTime

Time 0:00  — Background goroutine starts PollUntilDone (30 min context)

Time ~3:00 — Azure hits capacity issues; provisioning is failing
             ProvisioningState may still show "Creating" or "Updating"

Time 5:00  — MaxNodeProvisionTime expires (if set to 5 min)
             ClusterStateRegistry: scale-up request expired
             Nodes treated as LongUnregistered → cleanup triggered
             Error classified as TIMEOUT, not CAPACITY
             ⚠️ Real error masked — backoff uses wrong error class

Time ~8:00 — Azure finally sets ProvisioningState: "Failed" with capacity error details
             But CAS already processed this as a timeout
             instanceStatusFromVM() may now detect the failure
             But the scale-up request is already gone → double-counting possible

Time 30:00 — asyncContextTimeout fires in background goroutine
             PollUntilDone returns context.DeadlineExceeded
             Cache invalidated — but main loop already moved on
```

#### Consequences of Error Masking

1. **Wrong error class**: `MaxNodeProvisionTime` expiration registers a timeout, not `OutOfResourcesErrorClass`. This can affect:
   - Backoff duration (different error classes could warrant different backoff behavior)
   - Metrics and dashboards (capacity errors underreported, timeouts overreported)
   - Any logic that distinguishes capacity errors from other failures

2. **Priority expander impact**: If the real error is a capacity/stockout issue, the priority expander should fail over to a different SKU or zone. A generic timeout may not convey the same urgency.

3. **Partial success confusion**: If Azure provisioned 3 of 5 VMs before the real error, those 3 VMs may register in Kubernetes *after* `MaxNodeProvisionTime` expired. CAS now has nodes it wasn't expecting, and the scale-up request was already cleaned up as failed.

4. **Deallocate mode amplification**: For `BeginStart`, the problem is worse because:
   - There's no effective indirect recovery path (Section 2.2.2)
   - `MaxNodeProvisionTime` is the **only** timeout that fires, and it has no idea the error is a start failure vs a creation failure
   - The VM stays deallocated, and CAS may try to start it again next iteration

#### When This Is Most Visible

This error masking is most likely when:
- `MaxNodeProvisionTime` is set lower than the default 15 min (some clusters use 5–10 min for faster failure detection)
- Azure is experiencing capacity constraints (provisioning takes longer than usual)
- VM extensions (CSE) are slow to complete (extension provisioning can take 5–10+ min)
- Deallocate mode is used (start operations are typically faster than creation, but the lack of any recovery path means even short-duration failures cause prolonged impact)

#### Implication for Design

This reinforces the value of the operation tracker (Appendix A.3) with **soft timeouts** that trigger **eager cache refreshes**:
- At soft timeout (e.g., 5 min): force-refresh the instance cache to get Azure's current provisioning state
- If Azure reports `ProvisioningState: Failed`, register the **real error** before `MaxNodeProvisionTime` fires
- This way backoff gets the correct error class, and priority expander gets the right signal

It also suggests the operation tracker's hard timeout should be **shorter than or equal to `MaxNodeProvisionTime`**, so the tracker classifies the failure before the generic timeout path does.

### 2.5 How CAS Issues Scale-Up and Scale-Down Calls

Understanding the batching and multiplicity of API calls is important context for error handling design.

#### Scale-Up: One Node Group per Iteration (Usually), Multiple VMs per Call

By default, the scale-up orchestrator selects **one best node group** per loop iteration:

1. The `ScaleUpOrchestrator` evaluates all candidate node groups via binpacking simulation
2. The `ExpanderStrategy.BestOption()` picks **a single winning node group**
3. `IncreaseSize(delta)` is called once, where `delta` can be multiple VMs (e.g., 5 nodes needed → one call with `delta=5`)
4. On Azure VMSS, this translates to a **single `BeginCreateOrUpdate` call** that sets the new VMSS capacity — Azure provisions all `delta` VMs as part of that one LRO

```
Loop iteration:
  Orchestrator picks: nodegroup-A needs +5 nodes
  → IncreaseSize(5)
    → BeginCreateOrUpdate(vmss-A, capacity: oldSize + 5)    ← single Azure API call
    → Azure provisions 5 VMs as one LRO
```

**Exception — `--balance-similar-node-groups`**: When enabled, the orchestrator splits the requested nodes across similar node groups. This produces **multiple `ScaleUpInfo` entries**, each targeting a different VMSS:

```
Loop iteration (balanced across 3 zones):
  Orchestrator picks: 6 nodes needed, balanced across zone-1, zone-2, zone-3
  → IncreaseSize(2) on vmss-zone-1    ← one Azure API call
  → IncreaseSize(2) on vmss-zone-2    ← one Azure API call
  → IncreaseSize(2) on vmss-zone-3    ← one Azure API call
```

With `--parallel-scale-up=false` (default): these three calls execute **serially** — if vmss-zone-1 fails, zone-2 and zone-3 are **not attempted**.

With `--parallel-scale-up=true`: these three calls execute **concurrently** via goroutines — all are attempted regardless of individual failures.

**Summary**: Each `IncreaseSize` call maps to exactly one VMSS and one Azure LRO, but that single LRO provisions multiple VMs. The autoscaler typically issues 1 call per iteration, or N calls when balancing across N similar groups.

#### Scale-Down: Multiple Node Groups, Multiple Nodes per Group, Batched

Scale-down is more complex — multiple node groups can have nodes deleted in the same iteration, and each group can have multiple nodes:

1. The **Planner** identifies unneeded nodes across **all** node groups based on utilization thresholds
2. The **Actuator** splits them into `empty` (no pods) and `needDrain` (have evictable pods)
3. The **BudgetProcessor** enforces a max parallel deletion count across the cluster
4. The **GroupDeletionScheduler** groups nodes by node group and sends each batch to the **NodeDeletionBatcher**
5. The batcher calls `deleteNodesFromCloudProvider()` which calls `nodeGroup.DeleteNodes(nodes)` with **all nodes for that group at once**

```
Loop iteration:
  Planner identifies: node-A1, node-A2 (from vmss-A), node-B1 (from vmss-B)
  Budget allows: 3 deletions this iteration

  → vmss-A.DeleteNodes([node-A1, node-A2])
    → DeleteInstances({instanceIds: ["A1", "A2"]})
      → BeginDeleteInstances(vmss-A, [A1, A2])          ← single Azure API call, 2 VMs
      → go waitForDeleteInstances(poller)

  → vmss-B.DeleteNodes([node-B1])
    → DeleteInstances({instanceIds: ["B1"]})
      → BeginDeleteInstances(vmss-B, [B1])               ← single Azure API call, 1 VM
      → go waitForDeleteInstances(poller)
```

For Azure VMSS, `BeginDeleteInstances` accepts a **list of instance IDs**, so multiple VMs in the same VMSS are deleted in a single LRO. Different VMSS get separate API calls.

The `--node-deletion-batcher-interval` flag (default 0s) controls whether nodes are batched across time. At 0s, deletions happen immediately. At >0s, the batcher waits to accumulate nodes for the same group before issuing one batched call.

#### Scale-Down (Deallocate Mode, Fork): Same Pattern, Different API

In deallocate mode, the flow is identical except `DeleteNodes` routes to `deallocateInstances()` instead of `deleteInstances()`, calling `BeginDeallocate` with the instance IDs. The batching and per-group multiplicity remain the same.

#### Scale-Up (Deallocate Mode Reallocation, Fork): Mixed Pattern

When scaling up a deallocate-mode group that has stopped VMs:

```
IncreaseSize(delta=5), deallocated count = 3:
  → startInstances([inst-1, inst-2, inst-3])
    → BeginStart(vmss, [inst-1, inst-2, inst-3])         ← one LRO: restart 3 VMs
    → go waitForStartInstances(poller)

  → createOrUpdateInstances(vmss, capacity + 2)
    → BeginCreateOrUpdate(vmss, newCapacity)              ← one LRO: create 2 new VMs
    → go waitForCreateOrUpdateInstances(poller)
```

This produces **two concurrent LROs** for the same VMSS: one to start existing VMs, one to create new VMs. Both run in background goroutines. A tail-side error on either is handled (or not) independently.

#### Summary Table

| Operation | Node Groups per Iteration | VMs per API Call | Azure API Calls per Group | LRO Pattern |
|-----------|--------------------------|------------------|---------------------------|-------------|
| **Scale-up (default)** | 1 | 1–N (delta) | 1 `BeginCreateOrUpdate` | async goroutine |
| **Scale-up (balanced)** | 1–N (similar groups) | 1–M per group | 1 per group | async goroutine (serial or parallel) |
| **Scale-down (delete)** | 1–N (any unneeded) | 1–M per group (batched) | 1 `BeginDeleteInstances` per group | async goroutine |
| **Scale-down (deallocate)** | 1–N | 1–M per group | 1 `BeginDeallocate` per group | async goroutine |
| **Scale-up (reallocation)** | 1 | split: restart + create | 2 LROs (Start + CreateOrUpdate) | 2 async goroutines |

#### Implication for Tail-Side Errors

The batching model means a single tail-side LRO error can affect **multiple VMs at once**:
- A `BeginCreateOrUpdate` that fails after provisioning 3 of 5 requested VMs leaves 2 VMs never created but `curSize` reflecting all 5
- A `BeginDeleteInstances` that fails to delete 2 of 3 requested VMs leaves those 2 nodes still running but cache reflecting them as `InstanceDeleting`
- A `BeginStart` that fails to restart 3 deallocated VMs leaves them stopped, but with no backoff signal

This is why a future operation tracker (Appendix A.3) would track operations at the **VMSS + operation type** level rather than individual VM level — the LRO is the unit of work, and its outcome affects all VMs in the batch.

---

## 3. Background: VM Provisioning States and Power States

Understanding Azure VM states is critical for this design — the autoscaler's ability to detect tail-side errors depends entirely on how it interprets the combination of provisioning state and power state reported by Azure.

### 3.1 Provisioning States

The `provisioningState` property represents the status of the most recent control-plane operation on a VM. Source: [Azure VM States and Billing](https://learn.microsoft.com/en-us/azure/virtual-machines/states-billing).

| Provisioning State | Meaning | Typical Trigger |
|--------------------|---------|-----------------|
| `Creating` | VM is being created | `BeginCreateOrUpdate` (new capacity) |
| `Updating` | VM is updating to latest model; also set during start/restart | `BeginStart`, extension updates, reimage |
| `Succeeded` | Last operation completed successfully | Any operation that finishes without error |
| `Failed` | Last operation was unsuccessful | Allocation failure, extension error, timeout |
| `Deleting` | VM is being deleted | `BeginDeleteInstances` |
| `Migrating` | ASM → ARM migration (rare) | Platform migration |

**Key nuance**: `provisioningState` reflects the *most recent* operation, not a cumulative state. A VM that was successfully created (`Succeeded`) and then had an extension update fail will show `Failed` — even though the VM itself is running fine. This is why power state must be checked alongside provisioning state.

### 3.2 Power States

Power states reflect the VM's runtime status from the hypervisor, obtained from the Instance View.

| Power State | Meaning | Billed? |
|-------------|---------|---------|
| `PowerState/starting` | VM is powering up | Yes |
| `PowerState/running` | VM is fully up | Yes |
| `PowerState/stopping` | Transitional to stopped | Yes |
| `PowerState/stopped` | VM allocated on host but not running | Yes |
| `PowerState/deallocating` | Transitional to deallocated | No* |
| `PowerState/deallocated` | VM has released hardware lease | No* |

\* Compute billing stops; disk/networking costs continue.

### 3.3 State Combinations During Normal Operations

#### Delete Mode: Scale-Up (Create New VMs)

| Phase | Provisioning State | Power State | CAS Instance State |
|-------|-------------------|-------------|-------------------|
| LRO initiated | `Creating` | *(none yet)* | `InstanceCreating` |
| VM allocating | `Creating` | `starting` | `InstanceCreating` |
| VM running, extensions installing | `Updating` | `running` | Cached status preserved |
| VM ready | `Succeeded` | `running` | `InstanceRunning` |
| Allocation failed | `Failed` | *(none)* or `stopped` | See Section 3.4 |
| Extension failed | `Failed` | `running` | See Section 3.4 |

#### Delete Mode: Scale-Down (Delete VMs)

| Phase | Provisioning State | Power State | CAS Instance State |
|-------|-------------------|-------------|-------------------|
| LRO initiated | `Deleting` | `running` → `stopping` | `InstanceDeleting` |
| VM removed | *(VM no longer exists)* | *(N/A)* | Removed from instance list |
| Delete failed | Previous state restored | `running` | `InstanceRunning` (reappears) |

#### Deallocate Mode: Scale-Down (Deallocate VMs)

| Phase | Provisioning State | Power State | CAS Instance State |
|-------|-------------------|-------------|-------------------|
| LRO initiated | `Updating` | `running` → `deallocating` | `InstanceDeallocating` (fork) |
| VM deallocated | `Succeeded` | `deallocated` | `InstanceDeallocated` (fork) |
| Deallocation failed | `Failed` | `running` (never stopped) | `InstanceRunning` |

#### Deallocate Mode: Scale-Up (Start Deallocated VMs)

| Phase | Provisioning State | Power State | CAS Instance State |
|-------|-------------------|-------------|-------------------|
| LRO initiated | `Updating` | `deallocated` → `starting` | Transitional |
| VM running, extensions installing | `Updating` | `running` | Cached status preserved |
| VM ready | `Succeeded` | `running` | `InstanceRunning` |
| Start failed (capacity) | `Failed` | `deallocated` | **Gap**: not detected (see Section 5) |
| Start failed (extension) | `Failed` | `running` | **Gap**: treated as running VM (see Section 5) |
| Start timed out | `Failed` | varies | **Gap**: ambiguous state (see Section 5) |

### 3.4 How CAS Maps These States Today

The `instanceStatusFromVM()` function in `azure_scale_set_instance_cache.go` resolves provisioning state + power state into a CAS `InstanceStatus`:

```
0. ProvisioningState is nil or "Updating"
   → Return cached status (preserve proactive state from CAS operations)

1. ProvisioningState == "Deleting"  → InstanceDeleting

2. ProvisioningState == "Creating"  → InstanceCreating

3. ProvisioningState == "Failed":
   a. enableFastDeleteOnFailedProvisioning AND power state is NOT running:
      → InstanceCreating + ErrorInfo{OutOfResourcesErrorClass}
      → This triggers handleInstanceCreationErrors → backoff + cleanup
   b. Otherwise:
      → InstanceRunning (assumes the failure was non-fatal to the VM)

4. Default (Succeeded, Migrating, etc.):
   → InstanceRunning

5. CSE error check (if enableDetailedCSEMessage):
   → Override to InstanceCreating + ErrorInfo{vmssExtensionProvisioningFailed}
```

### 3.5 The Delete vs Deallocate Detection Gap

The key difference that causes problems:

**Delete mode** — When a newly created VM fails (`ProvisioningState: Failed` + non-running power state), the fast-delete path correctly identifies it as a creation failure. The VM never had a running power state, so the `!isRunningVmPowerState` check passes, `ErrorInfo` is set, and the downstream `handleInstanceCreationErrors` path triggers backoff + cleanup.

**Deallocate mode** — When a deallocated VM fails to restart, it can end up in two problematic states:

| State | Provisioning | Power | Fast-Delete Path Result | Problem |
|-------|-------------|-------|------------------------|---------|
| Failed to start (capacity) | `Failed` | `deallocated` | Would set `InstanceCreating + ErrorInfo` → triggers deletion | **Wrong action**: deletes a VM that should be kept for future reuse |
| Failed to start (extension) | `Failed` | `running` | Skipped (`isRunningVmPowerState` = true) | **No detection**: VM is broken but treated as healthy |
| Start timed out | `Failed` | `running` or `deallocated` | Depends on final power state | **Ambiguous**: may or may not be detected |

This is the core problem that Section 8 (Detailed Design) addresses — extending `instanceStatusFromVM()` to handle deallocate-mode failures distinctly from delete-mode failures.

---

## 4. Current State: Failure Taxonomy

### 3.1 Scale-Up Failures by Detection Point

| Failure | Phase | Detection Delay | Current Handling | Backoff Triggered? |
|---------|-------|----------------|------------------|--------------------|
| **Auth/permission denied** | Sync | Immediate | Error returned from `IncreaseSize` | ✅ Yes |
| **Invalid request / bad SKU** | Sync | Immediate | Error returned from `IncreaseSize` | ✅ Yes |
| **Immediate quota exceeded** | Sync | Immediate | Error returned from `IncreaseSize` | ✅ Yes |
| **Throttling (429)** | Sync | Immediate | Error returned from `IncreaseSize` | ✅ Yes |
| **Allocation failure (capacity)** | Async | 1–10 min | Cache refresh → `instanceStatusFromVM` | ⚠️ Only if `enableFastDeleteOnFailedProvisioning` |
| **CSE failure** | Async | 5–15 min | Cache refresh → `cseErrors()` | ⚠️ Only if `enableDetailedCSEMessage` |
| **Extension provisioning timeout** | Async | 30 min+ | `PollUntilDone` timeout → cache invalidation | ❌ No backoff; remediation via timeout |
| **Start deallocated VM fails** | Async | 5–30 min | **Not handled** — see [Section 5](#5-current-state-gaps-in-error-handling) | ❌ No |
| **Partial allocation** (3 of 5 VMs) | Async | 5–15 min | Successful VMs run; failed VMs detected via status | ⚠️ Partially |
| **VM created but never registers** | Timeout | 15 min (`MaxNodeProvisionTime`) | `LongUnregistered` detection → cleanup | ✅ Yes (delayed) |

### 3.2 Scale-Down Failures by Detection Point

| Failure | Phase | Detection Delay | Current Handling | Recovery |
|---------|-------|----------------|------------------|----------|
| **BeginDeleteInstances rejected** | Sync | Immediate | Error returned from `DeleteNodes` | Node untainted, retry next iteration |
| **Delete LRO fails** | Async | 5–30 min | Cache invalidation; node still exists | `fixNodeGroupSize`; node may be re-considered |
| **Deallocate LRO fails** | Async | 5–30 min | Cache invalidation | Node stays in ambiguous state |
| **Deallocate timeout** | Async | 30 min | Context deadline exceeded | Corrupted cluster state (see Gap 2) |

### 3.3 Deallocate Mode: Start Instance Failures

This is the most critical gap. When deallocated VMs fail to restart:

```
IncreaseSize(delta) [deallocate mode, instances available to restart]
  → startInstances(instanceIds)
    → BeginStart(ctx, rg, vmss, instanceIds)     ← initiates LRO
    → go waitForStartInstances(poller)            ← background goroutine
         │
         └─ PollUntilDone fails:
              - Capacity error → VM stays deallocated
              - Extension error → VM running but CSE failed
              - Timeout → context deadline exceeded
              → klog.Errorf(...)
              → invalidateInstanceCache()
              → [NO backoff, NO error signal to core]
```

The VM may remain deallocated (so CAS tries to start it again next iteration) or end up in a broken state (running with failed extensions, shutdown taint still applied).

---

## 5. Current State: Gaps in Error Handling

### Gap 1: Deallocate Mode Backoff ([AKS#5589](https://github.com/Azure/AKS/issues/5589))

**Problem**: When a deallocate-mode node group encounters capacity errors during `BeginStart`, CAS does not back off. It continues attempting to start the same failing VMs every iteration.

**Root cause**: The `instanceStatusFromVM()` path that detects `ProvisioningState: Failed` and triggers backoff via `handleInstanceCreationErrors` was designed for **new VM creation**, not for **restarting existing deallocated VMs**. The deallocated instance may show:
- `ProvisioningState: Failed` + `PowerState/deallocated` (restart failed, VM still stopped)
- But the fast-delete path would try to *delete* this VM, which is undesirable in deallocate mode — the whole point is to keep VMs for reuse

**Impact**: Priority expander failover doesn't trigger. Pods stay pending until manual intervention or a long remediation cycle.

### Gap 2: Start Instance Timeout

**Problem**: When `waitForStartInstances` times out (`context deadline exceeded` after `asyncContextTimeout` = 30 min), the VM may have extension provisioning errors that aren't detected. The VM is running but has the shutdown taint, and CAS's cluster state is corrupted.

**Root cause**: After timeout, the only recovery is cache invalidation. But if the VM is now `PowerState/running` with `ProvisioningState: Failed`, the fast-delete path sees a *running* VM and skips it (`isRunningVmPowerState` returns true). The VM stays in limbo: running but tainted, with failed extensions.

**Impact**: Remediation takes 45+ minutes. The node is not usable but is counted in the node group.

### Gap 3: Two Divergent Status Functions

**Problem**: VM provisioning state is handled by two separate functions:
- `instanceStatusFromVM()` in `azure_scale_set_instance_cache.go` — for uniform VMSS (both delete and deallocate modes)
- `instanceStatusFromProvisioningStateAndPowerState()` in `azure_scale_set.go` — for flex VMSS (delete mode only)

These functions have diverged across versions, making maintenance difficult and error-prone.

**Impact**: Bug fixes applied to one function may not be applied to the other. Deallocate mode support is not available for flex VMSS.

### Gap 4: No Direct Error Channel from Background Goroutines

**Problem**: The fundamental architectural limitation — background goroutines that poll LROs cannot return errors to the autoscaler core. All error information must flow through the instance cache, which is:
- Delayed (cache TTL)
- Lossy (not all error states are reflected in VM provisioning/power state)
- Indirect (requires matching VM states to error conditions)

**Impact**: Any tail-side error that doesn't result in a detectable VM state (`ProvisioningState: Failed` + non-running power state) is effectively invisible to the autoscaler core until `MaxNodeProvisionTime` expires.

---

## 6. Design Options

### Option A: Synchronous LRO Completion (Block Until Done)

Make `IncreaseSize` / `DeleteNodes` block until the Azure LRO completes, similar to GCE's approach.

**Implementation**: Replace `go waitForCreateOrUpdateInstances(poller)` with inline `poller.PollUntilDone(ctx, nil)`.

| Pros | Cons |
|------|------|
| Errors propagate directly to caller | Blocks the autoscaler loop during LRO completion |
| Simple error handling model | Loop iteration time increases from ~seconds to minutes |
| No cache consistency issues | Scale-down evaluation delayed while waiting for scale-up LRO |
| Backoff and failover work automatically | Single-group scale-up blocks the loop for the full LRO duration |

**Mitigating factor: `--parallel-scale-up`**. When combined with `--balance-similar-node-groups` and `--parallel-scale-up`, synchronous `IncreaseSize` calls execute in **parallel goroutines** via `executeScaleUpsParallel()`. Each goroutine blocks on its own LRO independently, and `wg.Wait()` waits for all to complete. The wall-clock time is the **longest** individual LRO, not the sum:

```
executeScaleUpsParallel() with sync IncreaseSize:
  goroutine 1: IncreaseSize(vmss-A) → PollUntilDone (3 min) → return error/nil
  goroutine 2: IncreaseSize(vmss-B) → PollUntilDone (5 min) → return error/nil
  goroutine 3: IncreaseSize(vmss-C) → PollUntilDone (2 min) → return error/nil
  wg.Wait() → wall-clock: 5 min (slowest), errors from ALL groups collected
```

This makes synchronous mode significantly more viable for multi-nodegroup clusters. Errors from each group propagate directly, enabling proper per-group backoff and failover. The main cost is that the loop iteration takes ~3–10 minutes instead of ~1 second, delaying scale-down evaluation.

**Verdict**: A viable approach when combined with `--parallel-scale-up`. Not recommended as the unconditional default (the loop cadence impact is still significant), but a strong option as a **per-nodegroup or per-cluster flag** for environments prioritizing error fidelity over loop responsiveness. Particularly attractive for deallocate-mode pools where start LROs are typically fast (1–3 min) and the current async path has no recovery.

### Option B: Error Channel from Background Goroutines

Add a structured error channel that background goroutines write to, and the main loop reads from at the start of each iteration.

**Implementation**:
```go
type AsyncOperationResult struct {
    NodeGroupID string
    Operation   string  // "CreateOrUpdate", "DeleteInstances", "Start", "Deallocate"
    Error       error
    Timestamp   time.Time
}

// ScaleSet gains a channel
type ScaleSet struct {
    // ... existing fields ...
    asyncErrors chan AsyncOperationResult
}
```

The main loop drains this channel at the start of `RunOnce()` and registers failures.

| Pros | Cons |
|------|------|
| Errors detected within one loop iteration | Requires plumbing a channel through cloud provider interface |
| Works for all operation types | Channel could back up if main loop is slow |
| Can trigger backoff + failover immediately | Need to handle channel lifecycle (close, drain) |
| Compatible with async model | CAS restart loses in-flight channel data |

**Verdict**: Attractive but requires changes to the `CloudProvider` interface or a new sidecar mechanism. Medium-term goal.

### Option C: Enhanced Instance Status Detection (Improve Indirect Path)

Make the existing indirect detection path faster, more comprehensive, and work for deallocate mode.

**Implementation**:
1. Invalidate instance cache **immediately** on background goroutine error (already done)
2. Ensure `instanceStatusFromVM()` correctly classifies all tail-side error states, including deallocate mode
3. Enable `enableFastDeleteOnFailedProvisioning` by default (or equivalent for deallocate mode)

| Pros | Cons |
|------|------|
| No interface changes needed | Still delayed by cache TTL (but can force refresh) |
| Works within existing architecture | Not all errors produce detectable VM states |
| Incremental improvement | Cache invalidation + re-fetch adds API calls |
| Can be rolled out per-flag | Doesn't solve the fundamental async gap |

**Verdict**: The pragmatic near-term approach. Addresses the most impactful issues (deallocate mode, start failures) without architectural changes.

### Option D: Operation Tracker with Timeout-Based Failure Detection

Introduce an **operation tracker** that records in-flight LROs and detects when they've been running too long, independent of the background goroutine.

Each Azure `Begin*` call returns a `runtime.Poller[T]` with a `ResumeToken()` — a JSON-serialized token containing the ARM operation's unique polling URL (includes a GUID). This serves as a natural unique key per LRO, even when multiple operations target the same VMSS concurrently (e.g., `BeginStart` + `BeginCreateOrUpdate` in deallocate mode):

**Implementation**:
```go
type TrackedOperation struct {
    ResumeToken   string         // unique per LRO, from poller.ResumeToken()
    ScaleSetID    string
    OperationType string
    StartTime     time.Time
    SoftTimeout   time.Duration  // triggers eager cache refresh
    HardTimeout   time.Duration  // triggers failure registration
    Completed     atomic.Bool
    Error         error          // set by background goroutine via MarkCompleted
}

type OperationTracker struct {
    mu         sync.Mutex
    operations map[string]*TrackedOperation  // keyed by ResumeToken
}
```

The background goroutine calls `MarkCompleted(resumeToken, err)` after `PollUntilDone` returns, so the tracker knows exactly which LRO finished. The main loop checks for timed-out operations and treats them as failures.

| Pros | Cons |
|------|------|
| Detects stuck operations proactively | Additional state to manage |
| Can have shorter timeouts than `asyncContextTimeout` | Timeout doesn't mean failure (operation may still succeed) |
| Works even if goroutine crashes/hangs | False positives possible |
| Survives goroutine panics | Need to reconcile with actual operation outcome |
| ResumeToken uniquely identifies each LRO | ResumeToken could be used to resume polling from main loop (future) |

**Verdict**: Good complement to Option C. Addresses the start timeout case (Gap 2) without requiring channel plumbing.

---

## 7. Recommended Approach: Enhanced Instance Status Detection (Option C)

We target **Option C** — improving the existing indirect detection path to properly handle failed provisioning across all scale-down modes. This requires no new infrastructure (no operation tracker, no error channels, no interface changes) and focuses on fixing the gaps in `instanceStatusFromVM()` and the downstream error handling.

### What We're Changing

1. **Fix `instanceStatusFromVM()` for deallocate mode** — Ensure failed-to-start deallocated VMs are classified as `InstanceCreating + ErrorInfo`, triggering backoff via the existing `handleInstanceCreationErrors` path
2. **Guard `deleteCreatedNodesWithErrors()` for deallocate mode** — Prevent the error cleanup path from deleting deallocated VMs that failed to start (they should be retried, not destroyed)
3. **Extend `enableFastDeleteOnFailedProvisioning` to deallocate mode** — The existing fast-delete path handles delete-mode failures but not deallocate-mode failures; add deallocate-aware logic before the existing check
4. **Unify status functions** — Merge the duplicate `instanceStatusFromProvisioningStateAndPowerState()` into `instanceStatusFromVM()` to prevent future divergence

### What We're NOT Changing (Yet)

- No operation tracker — detection relies on the existing instance cache refresh cycle
- No error channel — background goroutines continue to communicate only via cache invalidation
- No synchronous LRO mode — `IncreaseSize` continues to return quickly after `Begin*`

These are viable future enhancements documented in [Appendix A](#appendix-a-future-options).

For background on how VM provisioning states and power states map to CAS instance states, see [Section 3](#3-background-vm-provisioning-states-and-power-states).

---

## 8. Detailed Design

### 8.1 Enhanced Instance Status for Deallocate Mode

Modify `instanceStatusFromVM()` to properly handle deallocate-mode failure states:

```go
// Current code (simplified):
case VMProvisioningStateFailed:
    status.State = cloudprovider.InstanceRunning
    if scaleSet.enableFastDeleteOnFailedProvisioning {
        if !isRunningVmPowerState(powerState) {
            status.State = cloudprovider.InstanceCreating
            status.ErrorInfo = &cloudprovider.InstanceErrorInfo{...}
        }
    }

// Proposed addition:
case VMProvisioningStateFailed:
    status.State = cloudprovider.InstanceRunning

    // For deallocate mode: a failed-to-start VM should trigger backoff,
    // not deletion. Check if VM is deallocated with failed provisioning.
    if scaleSet.isDeallocateMode() && isDeallocatedPowerState(powerState) {
        // VM failed to start — remains deallocated. Signal this as a
        // creation error to trigger backoff without deletion.
        status.State = cloudprovider.InstanceCreating
        status.ErrorInfo = &cloudprovider.InstanceErrorInfo{
            ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
            ErrorCode:    "start-deallocated-failed",
            ErrorMessage: "Failed to start deallocated VM",
        }
    } else if scaleSet.enableFastDeleteOnFailedProvisioning {
        if !isRunningVmPowerState(powerState) {
            status.State = cloudprovider.InstanceCreating
            status.ErrorInfo = &cloudprovider.InstanceErrorInfo{
                ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
                ErrorCode:    "provisioning-state-failed",
                ErrorMessage: "Azure failed to provision a node",
            }
        }
    }
```

**Key distinction**: For deallocate mode, the recovery action should be **backoff** (try a different node group), not **delete** (destroy the VM we're trying to keep). The `handleInstanceCreationErrors` → `deleteCreatedNodesWithErrors` path needs to be aware that deallocate-mode instances with `start-deallocated-failed` should not be deleted (see 8.2).

### 8.2 Guard `deleteCreatedNodesWithErrors` for Deallocate Mode

The existing error cleanup path deletes VMs that failed to create. For deallocate mode, failed-to-start VMs should **not** be deleted:

```go
func (a *StaticAutoscaler) deleteCreatedNodesWithErrors() {
    nodesToDeleteByNodeGroupId := a.clusterStateRegistry.GetCreatedNodesWithErrors()
    for nodeGroupId, nodesToDelete := range nodesToDeleteByNodeGroupId {
        nodeGroup := nodeGroups[nodeGroupId]

        // NEW: Skip deletion for deallocate-mode instances that failed to start.
        // These should be retried later, not destroyed.
        if isDeallocateMode(nodeGroup) {
            nodesToDelete = filterOutDeallocatedInstances(nodeGroup, nodesToDelete)
        }

        if len(nodesToDelete) > 0 {
            if a.ForceDeleteFailedNodes {
                err = nodeGroup.ForceDeleteNodes(nodesToDelete)
            } else {
                err = nodeGroup.DeleteNodes(nodesToDelete)
            }
        }
    }
}
```

`filterOutDeallocatedInstances` checks each node's corresponding VM power state: if `PowerState/deallocated`, the node is excluded from deletion. VMs in other failed states (e.g., `PowerState/running` with CSE errors) may still warrant deletion.

### 8.3 Extend `enableFastDeleteOnFailedProvisioning` to Deallocate Mode

The `enableFastDeleteOnFailedProvisioning` flag already exists and is enabled by default in newer upstream versions. It correctly handles delete-mode node groups by converting `ProvisioningState: Failed` + non-running power state into `InstanceCreating + ErrorInfo`, triggering cleanup and backoff.

**The problem**: This flag's logic does not account for deallocate mode. When a deallocated VM fails to start, the fast-delete path either:
- Tries to **delete** the VM (wrong — it should be kept for reuse), or
- **Skips** detection entirely if the VM reached a running power state before the extension failed

**Change**: The fix in Section 8.1 extends the `ProvisioningState: Failed` handling to check for deallocate mode *before* the existing fast-delete path. This ensures deallocate-mode failures trigger backoff with a distinct error code (`start-deallocated-failed`) that the guard in Section 8.2 can filter on, rather than falling through to the delete-oriented fast-delete path.

### 8.4 Unify Status Functions

Consolidate `instanceStatusFromProvisioningStateAndPowerState()` into `instanceStatusFromVM()`:

```go
// BEFORE: Two functions
// 1. instanceStatusFromVM(vm *armcompute.VirtualMachineScaleSetVM) — for uniform VMSS
// 2. instanceStatusFromProvisioningStateAndPowerState(resourceID, provisioningState, powerState, enableFast) — for flex VMSS

// AFTER: Single function with explicit parameters
func (scaleSet *ScaleSet) resolveInstanceStatus(
    resourceID string,
    provisioningState *string,
    powerState string,
    instanceView *armcompute.VirtualMachineScaleSetVMInstanceView,
    cachedStatus *cloudprovider.InstanceStatus,
) *cloudprovider.InstanceStatus {
    // Unified logic handling:
    // - Updating → preserve cached status
    // - Creating → InstanceCreating
    // - Deleting → InstanceDeleting
    // - Failed → context-dependent (delete mode vs deallocate mode, running vs not)
    // - CSE errors → InstanceCreating + ErrorInfo
    // - Default → InstanceRunning
}
```

Both uniform and flex VMSS paths call this single function, passing the appropriate parameters. This eliminates the maintenance burden of keeping two functions in sync and ensures deallocate mode fixes apply to all VMSS types.

### 8.5 Monitoring These Changes

The existing CAS metrics and logs already cover the tail-side error detection pipeline. No new metrics are required — the fixes make **existing signals fire correctly** for deallocate mode, where they previously didn't.

#### Metrics to Watch

| Metric | Labels | What It Tells You |
|--------|--------|-------------------|
| `cluster_autoscaler_failed_scale_ups_total` | `reason` | Counts scale-up failures. After our fix, deallocate-mode start failures will increment this with reason `provisioning-state-failed` or `start-deallocated-failed`. **If this increases for deallocate pools after rollout, the fix is working.** |
| `cluster_autoscaler_node_group_backoff_status` | `node_group`, `reason` | Shows whether a node group is currently backed off (1) or not (0). After our fix, deallocate pools that hit capacity errors will show `1` here. |
| `cluster_autoscaler_node_group_healthiness` | `node_group` | Whether a node group is healthy (1) or not (0). Should remain `1` for deallocate pools with deliberately stopped VMs (the health check bypass in the fork handles this). |
| `cluster_autoscaler_function_duration_seconds` | `function=cloudProviderRefresh` | Duration of the cache refresh. Useful for detecting if the instance cache refresh is slow (which delays tail-side error detection). |
| `cluster_autoscaler_function_duration_seconds` | `function=scaleUp` | The wall-clock time of the scale-up phase. Since `IncreaseSize` returns quickly, a long duration here indicates the orchestrator/executor is doing more work (e.g., many groups evaluated). |
| `cluster_autoscaler_nodes_count` | `state=ready\|unready\|notStarted\|longUnregistered` | Node counts by state. `longUnregistered` increasing indicates VMs that never joined the cluster — the timeout-based safety net. After our fix, these should decrease for deallocate pools because backoff kicks in earlier. |
| `cluster_autoscaler_scaled_up_nodes_total` | *(none)* | Total nodes requested via `IncreaseSize`. Compare with `failed_scale_ups_total` to compute success rate. |
| `cluster_autoscaler_old_unregistered_nodes_removed_count` | *(none)* | Nodes cleaned up after failing to register. High values indicate tail-side errors that went undetected until `MaxNodeProvisionTime`. Should decrease after the fix. |
| `cluster_autoscaler_node_group_target_count` | `node_group` | Target size per group. For deallocate mode, compare with actual `nodes_count` to detect stale targets from failed starts. |
| `cluster_autoscaler_scale_down_in_cooldown` | *(none)* | Whether scale-down is in cooldown (1/0). Relevant for understanding if `--scale-down-delay-type-local` is in play. |

#### Key Log Messages

These are the existing log messages that indicate tail-side errors are being detected and handled. After the fix, the deallocate-mode variants should appear where they previously didn't:

**LRO completion (background goroutine)**:
```
# Scale-up LRO failed (already logged today):
E  "Failed to update the capacity for vmss %s with error %v, invalidate the cache so as to get the real size from API"

# Scale-up LRO succeeded:
I  "PollUntilDone for CreateOrUpdate(%s) success"

# Delete LRO failed:
E  "PollUntilDone for DeleteInstances(%v) for %s failed with error: %v"
```

**Instance status detection (next loop iteration)**:
```
# Fast-delete detects failed provisioning (delete mode — existing):
I  "VM %s reports failed provisioning state with non-running power state: %s"

# NEW after our fix — deallocate mode detection:
I  "VM %s reports failed provisioning state with deallocated power state: %s" (or similar)

# CSE error detection:
I  "VM %s reports CSE failure: %v, with provisioning state %s, power state %s"
```

**Backoff triggered**:
```
# This fires when a node group is backed off (already logged today):
W  "Disabling scale-up for node group %v until %v; errorClass=%v; errorCode=%v"

# After our fix, this will fire for deallocate pools with:
#   errorCode=start-deallocated-failed or provisioning-state-failed
```

**Error handling pipeline**:
```
# Instance creation errors detected:
I  "Failed adding %v nodes (%v unseen previously) to group %v due to %v; errorMessages=%#v"

# K8s event emitted:
W  Event(ScaleUpFailed): "Failed adding %v nodes to group %v due to %v; source errors: %v"
```

**Nodes cleaned up**:
```
# deleteCreatedNodesWithErrors:
I  "Deleting %v from %v node group because of create errors"

# After our fix, this should NOT fire for deallocate-mode instances that are simply
# deallocated (the guard filters them out). If it does fire, the guard isn't working.
```

#### Validating Across the Fleet

To validate the fix is working at scale:

1. **Before rollout**: Establish a baseline of `failed_scale_ups_total` by reason for deallocate-mode pools. Expect the count to be low (because failures aren't being detected).

2. **After rollout**: Expect `failed_scale_ups_total{reason="start-deallocated-failed"}` to **increase** — this means failures that were previously silent are now being caught. Also watch for:
   - `node_group_backoff_status` transitioning to `1` for affected pools
   - `old_unregistered_nodes_removed_count` **decreasing** (backoff prevents repeated futile scale-up attempts, so fewer nodes hit the timeout)
   - `ScaleUpFailed` k8s events appearing for deallocate pools

3. **Regression signal**: If `nodes_count{state=longUnregistered}` does NOT decrease, or `ScaleUpFailed` events don't appear for known-failing deallocate pools, the detection path is not working correctly.

---

## 9. Deallocate Mode Specific Fixes

### 9.1 Fix: Backoff on Start Failure ([AKS#5589](https://github.com/Azure/AKS/issues/5589))

**Current behavior**: `startInstances()` calls `BeginStart` in a background goroutine. On failure, only cache invalidation occurs. No backoff.

**Fix** (using the changes in Section 8):
1. `instanceStatusFromVM()` detects `ProvisioningState: Failed` + `PowerState/deallocated` on the instances that failed to start (Section 8.1). See [Section 3.5](#35-the-delete-vs-deallocate-detection-gap) for why this state combination is currently mishandled.
2. These are reported as `InstanceCreating` + `ErrorInfo` (creation error)
3. `handleInstanceCreationErrors()` picks them up and registers a failed scale-up → **backoff triggered**
4. `deleteCreatedNodesWithErrors()` **skips** these instances because the deallocate guard filters them out (Section 8.2)
5. Priority expander can now fail over to a different node group on the next iteration

**No new components needed** — this works entirely through the existing `handleInstanceCreationErrors` pipeline with the two targeted fixes from Section 8.

```go
// Guard in deleteCreatedNodesWithErrors:

```go
func (a *StaticAutoscaler) deleteCreatedNodesWithErrors() {
    nodesToDeleteByNodeGroupId := a.clusterStateRegistry.GetCreatedNodesWithErrors()
    for nodeGroupId, nodesToDelete := range nodesToDeleteByNodeGroupId {
        nodeGroup := nodeGroups[nodeGroupId]

        // NEW: Skip deletion for deallocate-mode instances that failed to start.
        // These should be retried later, not destroyed.
        if isDeallocateMode(nodeGroup) {
            nodesToDelete = filterOutDeallocatedInstances(nodeGroup, nodesToDelete)
        }

        if len(nodesToDelete) > 0 {
            err = nodeGroup.DeleteNodes(nodesToDelete)
        }
    }
}
```

### 9.2 Fix: Start Timeout Corrupted State

**Current behavior**: After `waitForStartInstances` times out (`context deadline exceeded`), the VM may be `PowerState/running` with `ProvisioningState: Failed` and extension errors. The shutdown taint persists. CAS sees a running VM that's actually broken.

**Fix**:
1. The `cleanUpTaintsFromDeallocatedNodes()` function in the fork should be extended to also handle VMs that are running but have extension errors:

```go
func (a *StaticAutoscaler) cleanUpTaintsFromDeallocatedNodes(allNodes []*apiv1.Node) {
    for _, node := range allNodes {
        // Existing: remove ToBeDeleted from shutdown/unreachable + deallocate-mode nodes
        if !(taints.HasShutdownTaint(node) || taints.HasUnreachableTaint(node)) || !taints.HasToBeDeletedTaint(node) {
            continue
        }
        // ... existing deallocate cleanup ...

        // NEW: Also check for running VMs with failed extensions in deallocate mode.
        // If VM is running but has CSE errors, it needs cleanup (either taint removal
        // + extension retry, or force delete + replacement).
    }
}
```

2. For the timeout case specifically: `MaxNodeProvisionTime` (15 min default) fires before `asyncContextTimeout` (30 min) and marks the node as `LongUnregistered`, triggering cleanup. This is the existing safety net. The fix in 8.1 makes **pre-timeout** detection faster — if Azure reports `Failed` before `MaxNodeProvisionTime` fires, backoff happens immediately rather than waiting for the timeout.

### 9.3 Fix: InstanceFailed State Removal

The fork introduced an `InstanceFailed` state that is not part of upstream's `InstanceState` enum. This creates compatibility issues and maintenance burden.

**Fix**: Replace `InstanceFailed` usage with the upstream-compatible pattern:
- Use `InstanceCreating` + `ErrorInfo` for instances that failed provisioning (triggers `handleInstanceCreationErrors`)
- Use `InstanceRunning` for instances that are functional despite a failed operation
- Use `InstanceDeallocated` (fork) for instances that are deliberately stopped

This aligns with the upstream approach and removes the need for a fork-specific state.

---

## 10. Open Questions

1. **What is the right behavior when a deallocate-mode VM fails to start repeatedly?** Should it be:
   - (a) Backed off like any other node group (current proposal)
   - (b) The specific deallocated VM marked as unhealthy while others in the VMSS are retried
   - (c) The VM force-deleted and a fresh one created instead

2. **How do we validate the `filterOutDeallocatedInstances` guard?** We need to ensure the VM power state check reliably distinguishes "deallocated VM that failed to start" from "new VM that failed to create."

---

## 11. References

### Code References
| Component | File |
|-----------|------|
| Instance status resolution | [azure_scale_set_instance_cache.go](cluster-autoscaler/cloudprovider/azure/azure_scale_set_instance_cache.go#L200-L277) |
| Flex VMSS status (to be unified) | [azure_scale_set.go](cluster-autoscaler/cloudprovider/azure/azure_scale_set.go#L828-L868) |
| Background goroutine: scale-up | [azure_scale_set.go](cluster-autoscaler/cloudprovider/azure/azure_scale_set.go#L465-L479) |
| Background goroutine: delete | [azure_scale_set.go](cluster-autoscaler/cloudprovider/azure/azure_scale_set.go#L573-L593) |
| Error detection in ClusterStateRegistry | [clusterstate.go](cluster-autoscaler/clusterstate/clusterstate.go#L1147-L1215) |
| Scale-up failure registration | [clusterstate.go](cluster-autoscaler/clusterstate/clusterstate.go#L334-L362) |
| Failed node cleanup | [static_autoscaler.go](cluster-autoscaler/core/static_autoscaler.go#L898-L958) |
| Exponential backoff | [exponential_backoff.go](cluster-autoscaler/utils/backoff/exponential_backoff.go) |

### External
- [Azure VM States and Billing](https://learn.microsoft.com/en-us/azure/virtual-machines/states-billing)
- [Azure VMSS Delete Instances API](https://learn.microsoft.com/en-us/rest/api/compute/virtual-machine-scale-sets/delete-instances)
- [Azure VMSS Deallocate API](https://learn.microsoft.com/en-us/rest/api/compute/virtual-machine-scale-sets/deallocate)

---

## Appendix A: Future Options

The following approaches were evaluated but are not targeted in this iteration. They remain viable for future work.

### A.1 Synchronous LRO Completion (Option A)

Make `IncreaseSize` / `DeleteNodes` block until the Azure LRO completes. When combined with `--parallel-scale-up`, multiple node groups block in parallel goroutines (wall-clock = slowest LRO, not sum). Particularly attractive for deallocate-mode pools where start LROs are fast (1–3 min).

**Trade-off**: Loop iteration time increases from ~seconds to minutes, delaying scale-down evaluation.

### A.2 Error Channel from Background Goroutines (Option B)

Add a structured `AsyncOperationResult` channel that background goroutines write to. The main loop drains this channel at the start of each `RunOnce()` and registers failures immediately.

```go
type AsyncOperationResult struct {
    NodeGroupID string
    Operation   string  // "CreateOrUpdate", "DeleteInstances", "Start", "Deallocate"
    Error       error
    Timestamp   time.Time
}
```

**Trade-off**: Requires plumbing a channel through the cloud provider interface or a new sidecar mechanism. CAS restart loses in-flight data.

### A.3 Operation Tracker with Timeout-Based Detection (Option D)

Introduce an operation tracker that records in-flight LROs and detects when they've exceeded a soft timeout (trigger eager cache refresh) or hard timeout (register failure).

Each Azure `Begin*` call returns a `runtime.Poller[T]` which exposes `ResumeToken()` — a JSON-serialized token containing the ARM operation's unique polling URL (includes a GUID operation ID). This is the natural key for tracking individual LROs:

```go
poller, err := client.BeginCreateOrUpdate(ctx, rg, vmss, spec, nil)
token, _ := poller.ResumeToken()
// token contains: {"type":"azure-async-operation","url":"https://management.azure.com/.../operations/{unique-guid}"}
```

Using this as the tracker key eliminates ambiguity when multiple LROs are in flight for the same VMSS (e.g., a `Start` and a `CreateOrUpdate` running concurrently in deallocate mode):

```go
type TrackedOperation struct {
    ResumeToken string        // unique per LRO, from poller.ResumeToken()
    Type        OperationType
    ScaleSetID  string
    StartTime   time.Time
    SoftTimeout time.Duration
    HardTimeout time.Duration
    Completed   atomic.Bool
    Result      error
}

type OperationTracker struct {
    mu         sync.Mutex
    operations map[string]*TrackedOperation  // keyed by ResumeToken
}
```

The background goroutine calls `MarkCompleted` with the same resume token after `PollUntilDone` returns, so the tracker knows exactly which LRO finished. The `ResumeToken` also enables a future enhancement: polling from the main loop via `runtime.NewPoller` with the saved token, decoupling status checks from the background goroutine entirely.

**Timeout values considered**:

| Operation | Soft Timeout (eager refresh) | Hard Timeout (register failure) |
|-----------|-----------------------------|---------------------------------|
| `CreateOrUpdate` | 5 min | 15 min (= `MaxNodeProvisionTime`) |
| `DeleteInstances` | 10 min | 30 min (= `asyncContextTimeout`) |
| `Start` (deallocate) | 5 min | 10 min |
| `Deallocate` | 10 min | 30 min |

**Trade-off**: Additional state to manage; timeout doesn't necessarily mean failure (false positives possible). Good complement to Option C if faster detection is needed beyond what instance status polling provides.

### A.4 Observability Enhancements

If needed in the future, these metrics and events would improve tail-side error visibility:

```go
// Metrics
azure_lro_duration_seconds{operation, nodegroup, result}  // histogram
azure_lro_errors_total{operation, nodegroup, error_code}  // counter
azure_lro_in_flight{operation, nodegroup}                 // gauge

// Kubernetes events
a.LogRecorder.Eventf(apiv1.EventTypeWarning, "LROTimeout",
    "Scale-up LRO for %s timed out after %v", nodeGroupID, duration)
a.LogRecorder.Eventf(apiv1.EventTypeWarning, "StartDeallocatedFailed",
    "Failed to start deallocated VM(s) in %s: %v", nodeGroupID, err)
```
