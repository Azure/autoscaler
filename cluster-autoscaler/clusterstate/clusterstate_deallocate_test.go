/*
Copyright 2016 The Kubernetes Authors.

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

package clusterstate

import (
	"testing"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/azure/deallocate"
	mockprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/mocks"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate/utils"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/processors/nodegroupconfig"
	"k8s.io/autoscaler/cluster-autoscaler/processors/nodegroups/asyncnodegroups"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	"k8s.io/client-go/kubernetes/fake"
	kube_record "k8s.io/client-go/tools/record"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestHandleInstanceCreationErrorsStartDeallocatedFailed verifies that deallocated VMs
// that fail to start trigger backoff with the expected Azure-specific error code.
func TestHandleInstanceCreationErrorsStartDeallocatedFailed(t *testing.T) {
	now := time.Now()

	provider := testprovider.NewTestCloudProviderBuilder().Build()
	mockedNodeGroup := &mockprovider.NodeGroup{}
	mockedNodeGroup.On("Id").Return("deallocate-pool")
	mockedNodeGroup.On("Autoprovisioned").Return(false)
	mockedNodeGroup.On("TargetSize").Return(2, nil)
	mockedNodeGroup.On("TemplateNodeInfo").Return(nil, cloudprovider.ErrNotImplemented)
	mockedNodeGroup.On("GetOptions", mock.Anything).Return(&config.NodeGroupAutoscalingOptions{
		MaxNodeProvisionTime: 15 * time.Minute,
	}, nil)
	mockedNodeGroup.On("Nodes").Return([]cloudprovider.Instance{
		{
			Id: "azure://subscriptions/test-sub/resourceGroups/test-rg/providers/Microsoft.Compute/virtualMachineScaleSets/deallocate-pool/virtualMachines/0",
			Status: &cloudprovider.InstanceStatus{
				State: cloudprovider.InstanceCreating,
				ErrorInfo: &cloudprovider.InstanceErrorInfo{
					ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
					ErrorCode:    "start-deallocated-failed",
					ErrorMessage: "Failed to start deallocated VM",
				},
			},
		},
		{
			Id: "azure://subscriptions/test-sub/resourceGroups/test-rg/providers/Microsoft.Compute/virtualMachineScaleSets/deallocate-pool/virtualMachines/1",
			Status: &cloudprovider.InstanceStatus{
				State: cloudprovider.InstanceCreating,
				ErrorInfo: &cloudprovider.InstanceErrorInfo{
					ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
					ErrorCode:    "start-deallocated-failed",
					ErrorMessage: "Failed to start deallocated VM",
				},
			},
		},
	}, nil)
	provider.InsertNodeGroup(mockedNodeGroup)

	fakeClient := &fake.Clientset{}
	fakeLogRecorder, _ := utils.NewStatusMapRecorder(fakeClient, "kube-system", kube_record.NewFakeRecorder(5), false, "my-cool-configmap")
	clusterstate := NewClusterStateRegistry(
		provider,
		ClusterStateRegistryConfig{},
		fakeLogRecorder,
		newBackoff(),
		nodegroupconfig.NewDefaultNodeGroupConfigProcessor(config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}),
		asyncnodegroups.NewDefaultAsyncNodeGroupStateChecker(),
	)
	clusterstate.RegisterScaleUp(mockedNodeGroup, 2, now)

	// UpdateNodes will trigger handleInstanceCreationErrors.
	err := clusterstate.UpdateNodes([]*apiv1.Node{}, nil, now)
	assert.NoError(t, err)

	// Verify the node group is in backoff with the correct error code.
	safety := clusterstate.NodeGroupScaleUpSafety(mockedNodeGroup, now)
	assert.False(t, safety.SafeToScale, "node group should not be safe to scale")
	assert.True(t, safety.BackoffStatus.IsBackedOff, "node group should be backed off after start-deallocated-failed")
	assert.Equal(t, "start-deallocated-failed", safety.BackoffStatus.ErrorInfo.ErrorCode)
	assert.Equal(t, cloudprovider.OutOfResourcesErrorClass, safety.BackoffStatus.ErrorInfo.ErrorClass)
}

type policyNodeGroup struct {
	*mockprovider.NodeGroup
	policy deallocate.ScaleDownPolicy
}

func (ng *policyNodeGroup) ScaleDownPolicy() deallocate.ScaleDownPolicy {
	return ng.policy
}

func TestScaleUpWithDeallocatedNodes(t *testing.T) {
	shutdown := []string{"node.cloudprovider.kubernetes.io/shutdown"}
	tests := []struct {
		name            string
		policy          deallocate.ScaleDownPolicy
		registered      bool
		taintKeys       []string
		outcome         string
		existingUnready bool
	}{
		{
			name: "retained node fails", policy: deallocate.Deallocate, registered: true,
			taintKeys: shutdown, outcome: "start-deallocated-failed",
		},
		{
			name: "retained node with both taints fails", policy: deallocate.Deallocate, registered: true,
			taintKeys: []string{"node.cloudprovider.kubernetes.io/shutdown", apiv1.TaintNodeUnreachable}, outcome: "start-deallocated-failed",
		},
		{
			name: "unregistered node fails", policy: deallocate.Deallocate,
			outcome: "start-deallocated-failed",
		},
		{
			name: "retained node times out", policy: deallocate.Deallocate, registered: true,
			taintKeys: shutdown, outcome: "timeout",
		},
		{
			name: "retained node becomes ready", policy: deallocate.Deallocate, registered: true,
			taintKeys: shutdown,
		},
		{
			name: "restart succeeds with unrelated unready node", policy: deallocate.Deallocate, registered: true,
			taintKeys: shutdown, existingUnready: true,
		},
		{
			name: "delete mode keeps existing completion behavior", policy: deallocate.Delete, registered: true,
			taintKeys: shutdown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			targetSize := 0
			instances := []cloudprovider.Instance{{Id: "vm1"}}
			provider := testprovider.NewTestCloudProviderBuilder().Build()
			group := &policyNodeGroup{NodeGroup: &mockprovider.NodeGroup{}, policy: tc.policy}
			group.On("Id").Return("restart-pool")
			group.On("Autoprovisioned").Return(false)
			group.On("TargetSize").Return(func() int { return targetSize }, nil)
			group.On("Nodes").Return(func() []cloudprovider.Instance {
				return append([]cloudprovider.Instance(nil), instances...)
			}, nil)
			group.On("GetOptions", mock.Anything).Return(&config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}, nil)
			node := BuildTestNode("vm1", 1000, 1000)
			node.CreationTimestamp = metav1.NewTime(now.Add(-96 * time.Hour))
			SetNodeReadyState(node, false, now.Add(-96*time.Hour))
			for _, key := range tc.taintKeys {
				node.Spec.Taints = append(node.Spec.Taints, apiv1.Taint{Key: key, Effect: apiv1.TaintEffectNoSchedule})
			}
			group.On("TemplateNodeInfo").Return(framework.NewTestNodeInfo(node), nil)
			provider.InsertNodeGroup(group)
			provider.AddNode(group.Id(), node)
			var nodes []*apiv1.Node
			if tc.registered {
				nodes = append(nodes, node)
				if tc.policy == deallocate.Deallocate {
					spare := node.DeepCopy()
					spare.Name = "spare"
					spare.Spec.ProviderID = "spare"
					provider.AddNode(group.Id(), spare)
					instances = append(instances, cloudprovider.Instance{Id: "spare"})
					nodes = append(nodes, spare)
				}
			}
			allowedUnready := 0
			if tc.existingUnready {
				existing := BuildTestNode("existing-unready", 1000, 1000)
				existing.CreationTimestamp = node.CreationTimestamp
				SetNodeReadyState(existing, false, now.Add(-time.Hour))
				provider.AddNode(group.Id(), existing)
				instances = append(instances, cloudprovider.Instance{Id: existing.Spec.ProviderID})
				nodes = append(nodes, existing)
				targetSize++
				allowedUnready++
			}
			recorder, err := utils.NewStatusMapRecorder(&fake.Clientset{}, "kube-system", kube_record.NewFakeRecorder(10), false, "status")
			require.NoError(t, err)
			clusterstate := NewClusterStateRegistry(provider, ClusterStateRegistryConfig{OkTotalUnreadyCount: allowedUnready}, recorder, newBackoff(), nodegroupconfig.NewDefaultNodeGroupConfigProcessor(config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}), asyncnodegroups.NewDefaultAsyncNodeGroupStateChecker())
			require.NoError(t, clusterstate.UpdateNodes(nodes, nil, now))
			assert.False(t, clusterstate.HasNodeGroupStartedScaleUp(group.Id()))
			assert.False(t, clusterstate.BackoffStatusForNodeGroup(group, now).IsBackedOff)
			if tc.registered && tc.policy == deallocate.Deallocate {
				readiness := clusterstate.GetClusterReadiness()
				assert.Len(t, readiness.Deallocated, 2)
				assert.Len(t, readiness.Unready, 2+allowedUnready)
				assert.True(t, clusterstate.IsClusterHealthy())
			}

			targetSize++
			clusterstate.RegisterScaleUp(group, 1, now)
			clusterstate.Recalculate()
			require.NoError(t, clusterstate.UpdateNodes(nodes, nil, now.Add(time.Second)))
			if tc.policy == deallocate.Delete {
				assert.False(t, clusterstate.HasNodeGroupStartedScaleUp(group.Id()))
				assert.False(t, clusterstate.BackoffStatusForNodeGroup(group, now.Add(time.Second)).IsBackedOff)
				return
			}
			assert.True(t, clusterstate.HasNodeGroupStartedScaleUp(group.Id()))
			assert.True(t, clusterstate.IsNodeGroupScalingUp(group.Id()))
			assert.False(t, clusterstate.BackoffStatusForNodeGroup(group, now.Add(time.Second)).IsBackedOff)
			finishTime := now.Add(3 * time.Second)
			switch tc.outcome {
			case "start-deallocated-failed":
				instances[0].Status = &cloudprovider.InstanceStatus{
					State: cloudprovider.InstanceCreating,
					ErrorInfo: &cloudprovider.InstanceErrorInfo{
						ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
						ErrorCode:    tc.outcome,
						ErrorMessage: "Failed to start deallocated VM",
					},
				}
			case "timeout":
				finishTime = now.Add(16 * time.Minute)
			default:
				// The cloud node lifecycle controller removes the shutdown taint once the node is Ready.
				node.Spec.Taints = nil
				SetNodeReadyState(node, true, finishTime)
			}
			require.NoError(t, clusterstate.UpdateNodes(nodes, nil, finishTime))
			assert.False(t, clusterstate.HasNodeGroupStartedScaleUp(group.Id()))
			safety := clusterstate.NodeGroupScaleUpSafety(group, finishTime)
			if tc.outcome == "" {
				assert.True(t, safety.SafeToScale)
				assert.False(t, safety.BackoffStatus.IsBackedOff)
				assert.Empty(t, clusterstate.GetScaleUpFailures())
				assert.True(t, clusterstate.IsClusterHealthy())
				assert.Equal(t, []string{"spare"}, clusterstate.GetClusterReadiness().Deallocated)
			} else {
				assert.False(t, safety.SafeToScale)
				require.True(t, safety.BackoffStatus.IsBackedOff)
				assert.Equal(t, tc.outcome, safety.BackoffStatus.ErrorInfo.ErrorCode)
				if tc.outcome == "start-deallocated-failed" {
					assert.Equal(t, cloudprovider.OutOfResourcesErrorClass, safety.BackoffStatus.ErrorInfo.ErrorClass)
				}
			}
		})
	}
}

// A running node with only the unreachable taint is classified as deallocated, but it must not
// keep a scale-up open after the new node is Ready.
func TestScaleUpCompletesWithUnreachableRunningNode(t *testing.T) {
	now := time.Now()
	targetSize := 1
	instances := []cloudprovider.Instance{{Id: "unreachable"}}
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	group := &policyNodeGroup{NodeGroup: &mockprovider.NodeGroup{}, policy: deallocate.Deallocate}
	group.On("Id").Return("pool")
	group.On("Autoprovisioned").Return(false)
	group.On("TargetSize").Return(func() int { return targetSize }, nil)
	group.On("Nodes").Return(func() []cloudprovider.Instance {
		return append([]cloudprovider.Instance(nil), instances...)
	}, nil)
	group.On("GetOptions", mock.Anything).Return(&config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}, nil)
	unreachable := BuildTestNode("unreachable", 1000, 1000)
	unreachable.CreationTimestamp = metav1.NewTime(now.Add(-96 * time.Hour))
	SetNodeReadyState(unreachable, false, now.Add(-time.Hour))
	unreachable.Spec.Taints = []apiv1.Taint{{Key: apiv1.TaintNodeUnreachable, Effect: apiv1.TaintEffectNoExecute}}
	group.On("TemplateNodeInfo").Return(framework.NewTestNodeInfo(unreachable), nil)
	provider.InsertNodeGroup(group)
	provider.AddNode(group.Id(), unreachable)
	recorder, err := utils.NewStatusMapRecorder(&fake.Clientset{}, "kube-system", kube_record.NewFakeRecorder(10), false, "status")
	require.NoError(t, err)
	clusterstate := NewClusterStateRegistry(provider, ClusterStateRegistryConfig{OkTotalUnreadyCount: 1}, recorder, newBackoff(), nodegroupconfig.NewDefaultNodeGroupConfigProcessor(config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}), asyncnodegroups.NewDefaultAsyncNodeGroupStateChecker())
	require.NoError(t, clusterstate.UpdateNodes([]*apiv1.Node{unreachable}, nil, now))
	assert.Equal(t, []string{"unreachable"}, clusterstate.GetClusterReadiness().Deallocated)

	targetSize++
	clusterstate.RegisterScaleUp(group, 1, now)
	added := BuildTestNode("added", 1000, 1000)
	added.CreationTimestamp = metav1.NewTime(now.Add(time.Minute))
	SetNodeReadyState(added, true, now.Add(2*time.Minute))
	provider.AddNode(group.Id(), added)
	instances = append(instances, cloudprovider.Instance{Id: added.Spec.ProviderID})
	nodes := []*apiv1.Node{unreachable, added}
	require.NoError(t, clusterstate.UpdateNodes(nodes, nil, now.Add(3*time.Minute)))
	assert.False(t, clusterstate.IsNodeGroupScalingUp(group.Id()))

	later := now.Add(16 * time.Minute)
	require.NoError(t, clusterstate.UpdateNodes(nodes, nil, later))
	assert.False(t, clusterstate.BackoffStatusForNodeGroup(group, later).IsBackedOff)
	assert.Empty(t, clusterstate.GetScaleUpFailures())
}
