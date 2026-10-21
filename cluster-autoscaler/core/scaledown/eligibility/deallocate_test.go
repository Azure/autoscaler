/*
Copyright 2024 The Kubernetes Authors.

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

package eligibility

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/azure/deallocate"
	mockprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/mocks"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	cloudproviderapi "k8s.io/cloud-provider/api"
)

type policyNodeGroup struct {
	cloudprovider.NodeGroup
	policy deallocate.ScaleDownPolicy
}

func (ng *policyNodeGroup) ScaleDownPolicy() deallocate.ScaleDownPolicy {
	return ng.policy
}

func TestShouldSkipDeletionWhenDeallocatedRequiresDeallocatePolicy(t *testing.T) {
	node := BuildTestNode("node", 1000, 1000)
	SetNodeReadyState(node, false, time.Time{})
	node.Spec.Taints = append(node.Spec.Taints, apiv1.Taint{Key: cloudproviderapi.TaintNodeShutdown})

	testCases := []struct {
		name      string
		nodeGroup cloudprovider.NodeGroup
		want      bool
	}{
		{
			name:      "non-policy node group",
			nodeGroup: &mockprovider.NodeGroup{},
			want:      false,
		},
		{
			name:      "delete policy",
			nodeGroup: &policyNodeGroup{NodeGroup: &mockprovider.NodeGroup{}, policy: deallocate.Delete},
			want:      false,
		},
		{
			name:      "deallocate policy",
			nodeGroup: &policyNodeGroup{NodeGroup: &mockprovider.NodeGroup{}, policy: deallocate.Deallocate},
			want:      true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, shouldSkipDeletionWhenDeallocated(testCase.nodeGroup, node))
		})
	}
}
