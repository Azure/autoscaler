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

package azure

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/azure/deallocate"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/vmclient/mockvmclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/vmssclient/mockvmssclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/vmssvmclient/mockvmssvmclient"
)

func TestDeallocateNodes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	vmssName := "test-asg"
	var vmssCapacity int64 = 3
	cases := []struct {
		name              string
		orchestrationMode compute.OrchestrationMode
		enableForceDelete bool
		scaleDownPolicy   deallocate.ScaleDownPolicy
	}{
		{
			name:              "uniform, force delete enabled, deallocate mode",
			orchestrationMode: compute.Uniform,
			enableForceDelete: true,
			scaleDownPolicy:   deallocate.Deallocate,
		},
		{
			name:              "uniform, force delete disabled, deallocate mode",
			orchestrationMode: compute.Uniform,
			enableForceDelete: false,
			scaleDownPolicy:   deallocate.Deallocate,
		},
		/* Flex + Deallocate is not supported yet
		{
			name:              "flexible, force delete enabled, deallocate mode",
			orchestrationMode: compute.Flexible,
			enableForceDelete: true,
			scaleDownPolicy:   deallocate.Deallocate,
		},
		{
			name:              "flexible, force delete disabled, deallocate mode",
			orchestrationMode: compute.Flexible,
			enableForceDelete: false,
			scaleDownPolicy:   deallocate.Deallocate,
		},
		*/
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orchMode := tc.orchestrationMode
			enableForceDelete := tc.enableForceDelete
			scaleDownPolicy := tc.scaleDownPolicy

			expectedVMSSVMs := newTestVMSSVMList(3)
			expectedVMs := newTestVMList(3)

			manager := newTestAzureManager(t)
			manager.config.EnableForceDelete = enableForceDelete
			expectedScaleSets := newTestVMSSList(vmssCapacity, vmssName, "eastus", orchMode)

			mockVMSSClient := mockvmssclient.NewMockInterface(ctrl)
			mockVMSSClient.EXPECT().List(gomock.Any(), manager.config.ResourceGroup).Return(expectedScaleSets, nil).Times(2)

			if scaleDownPolicy == deallocate.Delete {
				mockVMSSClient.EXPECT().DeleteInstancesAsync(gomock.Any(), manager.config.ResourceGroup, gomock.Any(), gomock.Any(), enableForceDelete).Return(nil, nil)
				mockVMSSClient.EXPECT().WaitForDeleteInstancesResult(gomock.Any(), gomock.Any(), manager.config.ResourceGroup).Return(&http.Response{StatusCode: http.StatusOK}, nil).AnyTimes()
			} else {
				mockVMSSClient.EXPECT().DeallocateInstancesAsync(gomock.Any(), manager.config.ResourceGroup, gomock.Any(), gomock.Any()).Return(nil, nil)
				mockVMSSClient.EXPECT().WaitForDeallocateInstancesResult(gomock.Any(), gomock.Any(), manager.config.ResourceGroup).Return(&http.Response{StatusCode: http.StatusOK}, nil).AnyTimes()
			}

			manager.azClient.virtualMachineScaleSetsClient = mockVMSSClient

			mockVMSSVMClient := mockvmssvmclient.NewMockInterface(ctrl)
			mockVMClient := mockvmclient.NewMockInterface(ctrl)

			if orchMode == compute.Uniform {
				mockVMSSVMClient.EXPECT().List(gomock.Any(), manager.config.ResourceGroup, "test-asg", gomock.Any()).Return(expectedVMSSVMs, nil).AnyTimes()
				manager.azClient.virtualMachineScaleSetVMsClient = mockVMSSVMClient
			} else {
				manager.config.EnableVmssFlexNodes = true
				mockVMClient.EXPECT().ListVmssFlexVMsWithoutInstanceView(gomock.Any(), "test-asg").Return(expectedVMs, nil).AnyTimes()
				manager.azClient.virtualMachinesClient = mockVMClient
			}

			err := manager.forceRefresh()
			assert.NoError(t, err)

			resourceLimiter := cloudprovider.NewResourceLimiter(
				map[string]int64{cloudprovider.ResourceNameCores: 1, cloudprovider.ResourceNameMemory: 10000000},
				map[string]int64{cloudprovider.ResourceNameCores: 10, cloudprovider.ResourceNameMemory: 100000000})
			provider, err := BuildAzureCloudProvider(manager, resourceLimiter)

			assert.NoError(t, err)

			registered := manager.RegisterNodeGroup(
				newTestScaleSet(manager, testASG))
			manager.explicitlyConfigured[testASG] = true

			assert.True(t, registered)
			err = manager.forceRefresh()
			assert.NoError(t, err)

			scaleSet, ok := provider.NodeGroups()[0].(*ScaleSet)
			assert.True(t, ok)

			targetSize, err := scaleSet.TargetSize()
			assert.NoError(t, err)
			assert.Equal(t, 3, targetSize)

			scaleSet.scaleDownPolicy = scaleDownPolicy

			// Perform the delete operation
			nodesToDelete := []*apiv1.Node{
				newApiNode(orchMode, 0),
				newApiNode(orchMode, 2),
			}
			err = scaleSet.DeleteNodes(nodesToDelete)
			assert.NoError(t, err)

			if scaleDownPolicy == deallocate.Delete {
				// create scale set with vmss capacity 1
				expectedScaleSets = newTestVMSSList(1, vmssName, "eastus", orchMode)
			} else {
				// create scale set with vmss capacity 3
				expectedScaleSets = newTestVMSSList(3, vmssName, "eastus", orchMode)
			}

			mockVMSSClient.EXPECT().List(gomock.Any(), manager.config.ResourceGroup).Return(expectedScaleSets, nil).AnyTimes()

			if orchMode == compute.Uniform {
				if scaleDownPolicy == deallocate.Delete {
					expectedVMSSVMs[0].ProvisioningState = to.StringPtr(provisioningStateDeleting)
					expectedVMSSVMs[2].ProvisioningState = to.StringPtr(provisioningStateDeleting)
				} else {
					// DeleteNodes above waits for results in a goroutine, and deallocate implementation (waitForDeallocateInstancesResult)
					// currently accesses cache and adjusts provisioning state directly, so need to lock the instanceMutex to avoid data races
					// (Locks around scaleSet.lastInstanceRefresh below are added for the same reason)
					scaleSet.instanceMutex.Lock()
					expectedVMSSVMs[0].ProvisioningState = to.StringPtr(provisioningStateSucceeded)
					expectedVMSSVMs[2].ProvisioningState = to.StringPtr(provisioningStateSucceeded)
					expectedVMSSVMs[0].InstanceView = &compute.VirtualMachineScaleSetVMInstanceView{Statuses: &[]compute.InstanceViewStatus{{Code: to.StringPtr(vmPowerStateDeallocating)}}}
					expectedVMSSVMs[2].InstanceView = &compute.VirtualMachineScaleSetVMInstanceView{Statuses: &[]compute.InstanceViewStatus{{Code: to.StringPtr(vmPowerStateDeallocating)}}}
					scaleSet.instanceMutex.Unlock()

				}
				mockVMSSVMClient.EXPECT().List(gomock.Any(), manager.config.ResourceGroup, "test-asg", gomock.Any()).Return(expectedVMSSVMs, nil).AnyTimes()
			} else {
				if scaleDownPolicy == deallocate.Delete {
					expectedVMs[0].ProvisioningState = to.StringPtr(provisioningStateDeleting)
					expectedVMs[2].ProvisioningState = to.StringPtr(provisioningStateDeleting)
				} else {
					// Flex + Deallocate is not supported yet; is not tested here, fail just in case
					assert.Fail(t, "flexible orchestration mode does not support deallocate")
				}
				mockVMClient.EXPECT().ListVmssFlexVMsWithoutInstanceView(gomock.Any(), "test-asg").Return(expectedVMs, nil).AnyTimes()
			}

			err = manager.forceRefresh()
			assert.NoError(t, err)

			// Ensure the the cached size has been proactively decremented by 2
			targetSize, err = scaleSet.TargetSize()
			assert.NoError(t, err)
			assert.Equal(t, 1, targetSize)

			// Ensure that the status for the instances is Deleting or Deallocated
			// lastInstanceRefresh is set to time.Now() to avoid resetting instanceCache.
			scaleSet.instanceMutex.Lock()
			scaleSet.lastInstanceRefresh = time.Now()
			scaleSet.instanceMutex.Unlock()

			instance0, found, err := scaleSet.getInstanceByProviderID(nodesToDelete[0].Spec.ProviderID)
			assert.True(t, found, true)
			assert.NoError(t, err)
			if scaleDownPolicy == deallocate.Delete {
				assert.Equal(t, cloudprovider.InstanceDeleting, instance0.Status.State)
			} else {
				assert.Equal(t, cloudprovider.InstanceDeallocating, instance0.Status.State)
			}

			// lastInstanceRefresh is set to time.Now() to avoid resetting instanceCache.
			scaleSet.instanceMutex.Lock()
			scaleSet.lastInstanceRefresh = time.Now()
			scaleSet.instanceMutex.Unlock()

			instance2, found, err := scaleSet.getInstanceByProviderID(nodesToDelete[1].Spec.ProviderID)
			assert.True(t, found, true)
			assert.NoError(t, err)
			if scaleDownPolicy == deallocate.Delete {
				assert.Equal(t, cloudprovider.InstanceDeleting, instance2.Status.State)
			} else {
				assert.Equal(t, cloudprovider.InstanceDeallocating, instance2.Status.State)
			}
		})
	}
}

func TestDeallocateModeWaitForStartInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	provider := newTestProvider(t)
	mockVMSSClient := mockvmssclient.NewMockInterface(ctrl)
	provider.azureManager.azClient.virtualMachineScaleSetsClient = mockVMSSClient

	expectedVMSSVMs := newTestVMSSVMList(3)
	var instances []cloudprovider.Instance
	for _, vm := range expectedVMSSVMs {
		instances = append(instances, cloudprovider.Instance{
			Id: azurePrefix + *vm.ID,
			Status: &cloudprovider.InstanceStatus{
				State: cloudprovider.InstanceRunning,
			},
		})
	}

	asg := &ScaleSet{
		manager:         provider.azureManager,
		minSize:         1,
		maxSize:         5,
		InstanceCache:   InstanceCache{instanceCache: instances},
		scaleDownPolicy: deallocate.Deallocate,
	}
	asg.Name = testASG
	resp := &http.Response{StatusCode: 200}

	t.Run("when vmssVM client returns no error on StartInstancesAsync()", func(t *testing.T) {
		requiredInstanceIDs := &compute.VirtualMachineScaleSetVMInstanceRequiredIDs{}
		mockVMSSClient.EXPECT().WaitForStartInstancesResult(gomock.Any(), gomock.Any(),
			asg.manager.config.ResourceGroup).Return(resp, nil).Times(1)
		asg.waitForStartInstances(nil, requiredInstanceIDs)
		for _, vm := range asg.instanceCache {
			assert.Equal(t, vm.Status.State, cloudprovider.InstanceRunning)
		}
	})

	t.Run("when vmssVM client returns error on StartInstancesAsync() with previously deallocated instances", func(t *testing.T) {
		mockVMSSVMClient := mockvmssvmclient.NewMockInterface(ctrl)
		provider.azureManager.azClient.virtualMachineScaleSetVMsClient = mockVMSSVMClient

		mockVMSSClient.EXPECT().WaitForStartInstancesResult(gomock.Any(), gomock.Any(),
			asg.manager.config.ResourceGroup).Return(resp, fmt.Errorf("some error message")).Times(1)

		asg.waitForStartInstances(nil, &compute.VirtualMachineScaleSetVMInstanceRequiredIDs{})

		// On failure with WaitForStartInstancesResult(), it invalidates the instanceCache.
		lastInstanceCacheRefreshTime := asg.lastInstanceRefresh
		assert.Lessf(t, lastInstanceCacheRefreshTime, time.Now(), "instanceCache should be invalidated")
	})
}
