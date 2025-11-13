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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/utils/gpu"
	"k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	GPULabel = "TestGPULabel/accelerator"
)

func TestFilterOutNodesWithUnreadyResources(t *testing.T) {
	start := time.Now()
	later := start.Add(10 * time.Minute)
	laterRFC3339 := later.Format(time.RFC3339)
	expectedReadiness := make(map[string]bool)
	expectedAnnotation := make(map[string]*string)
	gpuLabels := map[string]string{
		GPULabel: "nvidia-tesla-k80",
	}
	readyCondition := apiv1.NodeCondition{
		Type:               apiv1.NodeReady,
		Status:             apiv1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(later),
	}
	unreadyCondition := apiv1.NodeCondition{
		Type:               apiv1.NodeReady,
		Status:             apiv1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(later),
	}

	nodeGpuReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeGpuReady",
			Labels:            gpuLabels,
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	nodeGpuReady.Status.Allocatable[gpu.ResourceNvidiaGPU] = *resource.NewQuantity(1, resource.DecimalSI)
	nodeGpuReady.Status.Capacity[gpu.ResourceNvidiaGPU] = *resource.NewQuantity(1, resource.DecimalSI)
	expectedReadiness[nodeGpuReady.Name] = true
	expectedAnnotation[nodeGpuReady.Name] = nil

	nodeGpuUnready := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeGpuUnready",
			Labels:            gpuLabels,
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	nodeGpuUnready.Status.Allocatable[gpu.ResourceNvidiaGPU] = *resource.NewQuantity(0, resource.DecimalSI)
	nodeGpuUnready.Status.Capacity[gpu.ResourceNvidiaGPU] = *resource.NewQuantity(0, resource.DecimalSI)
	expectedReadiness[nodeGpuUnready.Name] = false
	expectedAnnotation[nodeGpuUnready.Name] = ptr.To(laterRFC3339)

	nodeDirectXReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeDirectXReady",
			Labels:            gpuLabels,
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	nodeDirectXReady.Status.Allocatable[gpu.ResourceDirectX] = *resource.NewQuantity(1, resource.DecimalSI)
	nodeDirectXReady.Status.Capacity[gpu.ResourceDirectX] = *resource.NewQuantity(1, resource.DecimalSI)
	expectedReadiness[nodeDirectXReady.Name] = true
	expectedAnnotation[nodeDirectXReady.Name] = nil

	nodeDirectXUnready := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeDirectXUnready",
			Labels:            gpuLabels,
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	nodeDirectXUnready.Status.Allocatable[gpu.ResourceDirectX] = *resource.NewQuantity(0, resource.DecimalSI)
	nodeDirectXUnready.Status.Capacity[gpu.ResourceDirectX] = *resource.NewQuantity(0, resource.DecimalSI)
	expectedReadiness[nodeDirectXUnready.Name] = false
	expectedAnnotation[nodeDirectXUnready.Name] = ptr.To(laterRFC3339)

	nodeGpuUnready2 := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeGpuUnready2",
			Labels:            gpuLabels,
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Conditions: []apiv1.NodeCondition{readyCondition},
		},
	}
	expectedReadiness[nodeGpuUnready2.Name] = false
	expectedAnnotation[nodeGpuUnready2.Name] = ptr.To(laterRFC3339)

	nodeNoGpuReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeNoGpuReady",
			Labels:            make(map[string]string),
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Conditions: []apiv1.NodeCondition{readyCondition},
		},
	}
	expectedReadiness[nodeNoGpuReady.Name] = true
	expectedAnnotation[nodeNoGpuReady.Name] = nil

	nodeNoGpuUnready := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nodeNoGpuUnready",
			Labels:            make(map[string]string),
			CreationTimestamp: metav1.NewTime(start),
		},
		Status: apiv1.NodeStatus{
			Conditions: []apiv1.NodeCondition{unreadyCondition},
		},
	}
	expectedReadiness[nodeNoGpuUnready.Name] = false
	expectedAnnotation[nodeNoGpuUnready.Name] = nil

	initialReadyNodes := []*apiv1.Node{
		nodeGpuReady,
		nodeGpuUnready,
		nodeGpuUnready2,
		nodeDirectXReady,
		nodeDirectXUnready,
		nodeNoGpuReady,
	}
	initialAllNodes := []*apiv1.Node{
		nodeGpuReady,
		nodeGpuUnready,
		nodeGpuUnready2,
		nodeDirectXReady,
		nodeDirectXUnready,
		nodeNoGpuReady,
		nodeNoGpuUnready,
	}

	processor := GpuCustomResourcesProcessor{}
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	ctx := &context.AutoscalingContext{CloudProvider: provider}
	newAllNodes, newReadyNodes := processor.FilterOutNodesWithUnreadyResources(ctx, initialAllNodes, initialReadyNodes, nil)

	foundInReady := make(map[string]bool)
	for _, node := range newReadyNodes {
		foundInReady[node.Name] = true
		assert.True(t, expectedReadiness[node.Name], fmt.Sprintf("Node %s found in ready nodes list (it shouldn't be there)", node.Name))
	}
	for nodeName, expected := range expectedReadiness {
		if expected {
			assert.True(t, foundInReady[nodeName], fmt.Sprintf("Node %s expected ready, but not found in ready nodes list", nodeName))
		}
	}
	for _, node := range newAllNodes {
		assert.Equal(t, len(node.Status.Conditions), 1)
		if expectedReadiness[node.Name] {
			assert.Equal(t, node.Status.Conditions[0].Status, apiv1.ConditionTrue, fmt.Sprintf("Unexpected ready condition value for node %s", node.Name))
		} else {
			assert.Equal(t, node.Status.Conditions[0].Status, apiv1.ConditionFalse, fmt.Sprintf("Unexpected ready condition value for node %s", node.Name))
		}
		lastTransitionTimeAnnotation := node.Annotations[kubernetes.NodeReadyLastTranistionTimeAnnotationKey]
		if expectedAnnotation[node.Name] == nil {
			assert.Empty(t, lastTransitionTimeAnnotation, fmt.Sprintf("Node %s should not have last transition time annotation", node.Name))
		} else {
			assert.NotEmpty(t, lastTransitionTimeAnnotation, fmt.Sprintf("Node %s should have last transition time annotation", node.Name))
			assert.Equal(t, lastTransitionTimeAnnotation, *expectedAnnotation[node.Name], fmt.Sprintf("Unexpected last transition time annotation for node %s: expected %v, got %v", node.Name, expectedAnnotation[node.Name], lastTransitionTimeAnnotation))
		}
	}
}
