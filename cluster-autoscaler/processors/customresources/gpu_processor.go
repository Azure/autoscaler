/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package customresources

import (
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	ca_context "k8s.io/autoscaler/cluster-autoscaler/context"
	csisnapshot "k8s.io/autoscaler/cluster-autoscaler/simulator/csi/snapshot"
	drasnapshot "k8s.io/autoscaler/cluster-autoscaler/simulator/dynamicresources/snapshot"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/gpu"
	"k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	"k8s.io/klog/v2"
)

// GpuCustomResourcesProcessor handles only the GPU custom resource. It assumes,
// that the GPU may not become allocatable immediately after the node creation.
// It uses additional hacks to predict the type/count of GPUs in that case.
type GpuCustomResourcesProcessor struct {
}

// FilterOutNodesWithUnreadyResources removes nodes that should have GPU, but don't have
// it in allocatable from ready nodes list and updates their status to unready on all nodes list.
// This is a hack/workaround for nodes with GPU coming up without installed drivers, resulting
// in GPU missing from their allocatable and capacity.
func (p *GpuCustomResourcesProcessor) FilterOutNodesWithUnreadyResources(autoscalingCtx *ca_context.AutoscalingContext, allNodes, readyNodes []*apiv1.Node, _ *drasnapshot.Snapshot, _ *csisnapshot.Snapshot) ([]*apiv1.Node, []*apiv1.Node) {
	newAllNodes := make([]*apiv1.Node, 0)
	newReadyNodes := make([]*apiv1.Node, 0)
	nodesWithUnreadyGpu := make(map[string]*apiv1.Node)
	for _, node := range readyNodes {
		if gpuExposedViaDra(autoscalingCtx, node) {
			newReadyNodes = append(newReadyNodes, node)
			continue
		}

		_, hasGpuLabel := node.Labels[autoscalingCtx.CloudProvider.GPULabel()]
		hasAnyGpuAllocatable := gpu.NodeHasGpuResourceAllocatable(node)
		if hasGpuLabel && !hasAnyGpuAllocatable {
			klog.V(3).Infof("Overriding status of node %v, which seems to have unready GPU",
				node.Name)
			nodesWithUnreadyGpu[node.Name] = kubernetes.GetUnreadyNodeCopy(node, kubernetes.ResourceUnready)
		} else {
			newReadyNodes = append(newReadyNodes, node)
		}
	}
	// Override any node with unready GPU with its "unready" copy
	for _, node := range allNodes {
		if newNode, found := nodesWithUnreadyGpu[node.Name]; found {
			newAllNodes = append(newAllNodes, newNode)
		} else {
			newAllNodes = append(newAllNodes, node)
		}
	}
	return newAllNodes, newReadyNodes
}

// GetNodeResourceTargets returns mapping of resource names to their targets.
// This includes resources which are not yet ready to use and visible in kubernetes.
func (p *GpuCustomResourcesProcessor) GetNodeResourceTargets(autoscalingCtx *ca_context.AutoscalingContext, node *apiv1.Node, nodeGroup cloudprovider.NodeGroup) ([]CustomResourceTarget, errors.AutoscalerError) {
	gpuTarget, err := p.GetNodeGpuTarget(autoscalingCtx, node, nodeGroup)
	return []CustomResourceTarget{gpuTarget}, err
}

// GetNodeGpuTarget returns the physical GPU quota target for a node, including
// GPUs expected from the node group template but not visible as whole-GPU
// allocatable resources on the node.
func (p *GpuCustomResourcesProcessor) GetNodeGpuTarget(autoscalingCtx *ca_context.AutoscalingContext, node *apiv1.Node, nodeGroup cloudprovider.NodeGroup) (CustomResourceTarget, errors.AutoscalerError) {
	gpuLabel, found := node.Labels[autoscalingCtx.CloudProvider.GPULabel()]
	if !found {
		return CustomResourceTarget{}, nil
	}

	if gpuExposedViaDra(autoscalingCtx, node) {
		return CustomResourceTarget{}, nil
	}

	if gpuAllocatableValue, hasGpuAllocatable := gpu.NodeHasGpuAllocatable(node); hasGpuAllocatable {
		return CustomResourceTarget{gpuLabel, gpuAllocatableValue}, nil
	}

	// A node is supposed to have GPUs based on its label, but its allocatable
	// resources don't expose a physical GPU count. This can happen while GPU
	// resources are still becoming available, or when the node exposes only MIG
	// or time-sliced resources whose quantities represent slices or replicas.
	// GPU labels identify the GPU type, but don't provide the physical GPU count,
	// so use the node group template as the source of that count.

	if nodeGroup == nil {
		// Unmanaged nodes have no template from which to recover the physical GPU
		// count. Exclude the node from GPU quota usage rather than blocking
		// cluster-wide scale-ups. This can temporarily undercount GPU usage and
		// allow overprovisioning.
		klog.Warningf("Unable to determine physical GPU capacity for unmanaged node %q; excluding it from GPU quota usage", node.Name)
		return CustomResourceTarget{}, nil
	}

	template, err := nodeGroup.TemplateNodeInfo()
	if err != nil {
		klog.Errorf("Failed to build template for getting GPU estimation for node %v: %v", node.Name, err)
		return CustomResourceTarget{}, errors.ToAutoscalerError(errors.CloudProviderError, err)
	}
	for _, gpuVendorResourceName := range gpu.GPUVendorResourceNames {
		if gpuCapacity, found := template.Node().Status.Capacity[gpuVendorResourceName]; found {
			return CustomResourceTarget{gpuLabel, gpuCapacity.Value()}, nil
		}
	}

	// If the template doesn't define physical GPU capacity, omit it from quota usage.
	klog.Warningf("Template does not define physical GPU capacity even though node has a GPU label; node=%v", node.Name)
	return CustomResourceTarget{}, nil
}

// CleanUp cleans up processor's internal structures.
func (p *GpuCustomResourcesProcessor) CleanUp() {
}

func gpuExposedViaDra(autoscalingCtx *ca_context.AutoscalingContext, node *apiv1.Node) bool {
	gpuConfig := autoscalingCtx.CloudProvider.GetNodeGpuConfig(node)
	if gpuConfig == nil {
		return false
	}

	// Devices attached through DRA are not using node allocatable
	// to confirm their attachment, assume that node is ready
	// and will be checked in the separate processor
	return gpuConfig.ExposedViaDra()
}
