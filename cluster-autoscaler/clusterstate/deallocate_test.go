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

	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/processors/nodegroupconfig"
	"k8s.io/autoscaler/cluster-autoscaler/processors/nodegroups/asyncnodegroups"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate/utils"

	"k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	"k8s.io/client-go/kubernetes/fake"
	kube_record "k8s.io/client-go/tools/record"

	"github.com/stretchr/testify/assert"
)

func TestNotStartedDeallocate(t *testing.T) {
	now := time.Now()

	// Test two nodes that are
	// * in deallocate NG, NotReady,
	// * have CreationTimeStamp of more than MaxNotReadyNodeCreationTime ago
	// * are NotReady (with last transition time equal to the CreationTimestamp - that's what GPU filter does)
	// * and have LastTransitionToReady annotation set to relatively recent time
	// Both should initially be categorized as NotStarted
	//
	// One transitions to Ready and then has the NotReady taint removed - should move to Ready state
	// Another never transitions to Ready - should become Unready after MaxNotReadyNodeCreationTime
	//
	// Third node does not have the LastTransitionToReady annotation (was not forced to Unready by GPU) - should be treated as Unready right away

	ng1_1 := BuildTestNode("ng1-1", 1000, 1000)
	ng1_1.CreationTimestamp = metav1.Time{Time: now.Add(-30 * time.Minute)}
	SetNodeReadyState(ng1_1, false, ng1_1.CreationTimestamp.Time)
	SetNodeNotReadyTaint(ng1_1)
	kubernetes.RecordNodeReadyLastTransitonTime(ng1_1, metav1.Time{Time: now.Add(-2 * time.Minute)})

	ng1_2 := BuildTestNode("ng1-2", 1000, 1000)
	ng1_2.CreationTimestamp = metav1.Time{Time: now.Add(-30 * time.Minute)}
	SetNodeReadyState(ng1_2, false, ng1_2.CreationTimestamp.Time)
	SetNodeNotReadyTaint(ng1_2)
	kubernetes.RecordNodeReadyLastTransitonTime(ng1_2, metav1.Time{Time: now.Add(-2 * time.Minute)})

	ng1_3 := BuildTestNode("ng1-3", 1000, 1000)
	ng1_3.CreationTimestamp = metav1.Time{Time: now.Add(-30 * time.Minute)}
	SetNodeReadyState(ng1_3, false, now.Add(-10*time.Minute))
	SetNodeNotReadyTaint(ng1_3)

	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddDeallocateNodeGroup("ng1", 1, 10, 1)
	provider.AddNode("ng1", ng1_1)
	provider.AddNode("ng1", ng1_2)
	provider.AddNode("ng1", ng1_3)

	assert.NotNil(t, provider)
	fakeClient := &fake.Clientset{}
	fakeLogRecorder, _ := utils.NewStatusMapRecorder(fakeClient, "kube-system", kube_record.NewFakeRecorder(5), false, "some-map")
	clusterstate := NewClusterStateRegistry(provider, ClusterStateRegistryConfig{
		MaxTotalUnreadyPercentage: 10,
		OkTotalUnreadyCount:       1,
	}, fakeLogRecorder, newBackoff(), nodegroupconfig.NewDefaultNodeGroupConfigProcessor(config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 15 * time.Minute}), asyncnodegroups.NewDefaultAsyncNodeGroupStateChecker())
	err := clusterstate.UpdateNodes([]*apiv1.Node{ng1_1, ng1_2, ng1_3}, nil, now)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(clusterstate.GetClusterReadiness().Ready))
	assert.Equal(t, 2, len(clusterstate.GetClusterReadiness().NotStarted))
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().Unready))

	// node ng1_1 moves condition to ready
	SetNodeReadyState(ng1_1, true, now.Add(-1*time.Minute))
	err = clusterstate.UpdateNodes([]*apiv1.Node{ng1_1, ng1_2, ng1_3}, nil, now)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(clusterstate.GetClusterReadiness().Ready))
	assert.Equal(t, 2, len(clusterstate.GetClusterReadiness().NotStarted))
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().Unready))

	// node ng1_1 no longer has the taint
	RemoveNodeNotReadyTaint(ng1_1)
	err = clusterstate.UpdateNodes([]*apiv1.Node{ng1_1, ng1_2, ng1_3}, nil, now)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().Ready))
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().NotStarted))
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().Unready))

	// after 15 minutes, ng1_2 should be reclassified as Unready
	err = clusterstate.UpdateNodes([]*apiv1.Node{ng1_1, ng1_2, ng1_3}, nil, now.Add(16*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(clusterstate.GetClusterReadiness().Ready))
	assert.Equal(t, 0, len(clusterstate.GetClusterReadiness().NotStarted))
	assert.Equal(t, 2, len(clusterstate.GetClusterReadiness().Unready))
}
