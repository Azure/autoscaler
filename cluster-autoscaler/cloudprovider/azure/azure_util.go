/*
Copyright 2017 The Kubernetes Authors.

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
	"regexp"

	klog "k8s.io/klog/v2"
)

const (
	// Field names
	customDataFieldName      = "customData"
	dependsOnFieldName       = "dependsOn"
	hardwareProfileFieldName = "hardwareProfile"
	imageReferenceFieldName  = "imageReference"
	nameFieldName            = "name"
	osProfileFieldName       = "osProfile"
	propertiesFieldName      = "properties"
	resourcesFieldName       = "resources"
	storageProfileFieldName  = "storageProfile"
	typeFieldName            = "type"
	vmSizeFieldName          = "vmSize"

	// ARM resource Types
	nsgResourceType = "Microsoft.Network/networkSecurityGroups"
	rtResourceType  = "Microsoft.Network/routeTables"
	vmResourceType  = "Microsoft.Compute/virtualMachines"
	vmExtensionType = "Microsoft.Compute/virtualMachines/extensions"

	// CSE Extension checks
	vmssCSEExtensionName            = "vmssCSE"
	vmssExtensionProvisioningFailed = "VMExtensionProvisioningFailed"

	// resource ids
	nsgID = "nsgID"
	rtID  = "routeTableID"

	k8sLinuxVMNamingFormat         = "^[0-9a-zA-Z]{3}-(.+)-([0-9a-fA-F]{8})-{0,2}(\\d+)$"
	k8sLinuxVMAgentPoolNameIndex   = 1
	k8sLinuxVMAgentClusterIDIndex  = 2
	k8sLinuxVMAgentIndexArrayIndex = 3

	k8sWindowsOldVMNamingFormat = "^([a-fA-F0-9]{5})([0-9a-zA-Z]{3})(9)([a-zA-Z0-9]{3,5})$"
	k8sWindowsVMNamingFormat    = "^([a-fA-F0-9]{4})([0-9a-zA-Z]{3})(\\d{3,8})$"

	nodeLabelTagName     = "k8s.io_cluster-autoscaler_node-template_label_"
	nodeTaintTagName     = "k8s.io_cluster-autoscaler_node-template_taint_"
	nodeResourcesTagName = "k8s.io_cluster-autoscaler_node-template_resources_"
	nodeOptionsTagName   = "k8s.io_cluster-autoscaler_node-template_autoscaling-options_"

	// PowerStates reflect the operational state of a VM
	// From https://learn.microsoft.com/en-us/java/api/com.microsoft.azure.management.compute.powerstate?view=azure-java-stable
	vmPowerStatePrefix       = "PowerState/"
	vmPowerStateStarting     = "PowerState/starting"
	vmPowerStateRunning      = "PowerState/running"
	vmPowerStateStopping     = "PowerState/stopping"
	vmPowerStateStopped      = "PowerState/stopped"
	vmPowerStateDeallocating = "PowerState/deallocating"
	vmPowerStateDeallocated  = "PowerState/deallocated"
	vmPowerStateUnknown      = "PowerState/unknown"
)

var (
	vmnameLinuxRegexp        = regexp.MustCompile(k8sLinuxVMNamingFormat)
	vmnameWindowsRegexp      = regexp.MustCompile(k8sWindowsVMNamingFormat)
	oldvmnameWindowsRegexp   = regexp.MustCompile(k8sWindowsOldVMNamingFormat)
	azureResourceGroupNameRE = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(.+)/providers/(?:.*)`)
)

// AzUtil consists of utility functions which utilizes clients to different services.
// Since they span across various clients they cannot be fitted into individual client structs
// so adding them here.
// This struct is consumed by AKSAgentPool.
type AzUtil struct {
	manager *AzureManager
}

// DeleteVirtualMachine deletes a VM and any associated OS disk
func (util *AzUtil) DeleteVirtualMachine(rg, name string) error {
	ctx, cancel := getContextWithCancel()
	defer cancel()

	vm, rerr := util.manager.azClient.virtualMachinesClient.Get(ctx, rg, name, "")
	if rerr != nil {
		if exists, _ := checkResourceExistsFromRetryError(rerr); !exists {
			klog.V(2).Infof("VirtualMachine %s/%s has already been removed", rg, name)
			return nil
		}

		klog.Errorf("failed to get VM: %s/%s: %s", rg, name, rerr.Error())
		return rerr.Error()
	}

	vhd := vm.VirtualMachineProperties.StorageProfile.OsDisk.Vhd
	managedDisk := vm.VirtualMachineProperties.StorageProfile.OsDisk.ManagedDisk
	if vhd == nil && managedDisk == nil {
		klog.Errorf("failed to get a valid os disk URI for VM: %s/%s", rg, name)
		return fmt.Errorf("os disk does not have a VHD URI")
	}

	osDiskName := vm.VirtualMachineProperties.StorageProfile.OsDisk.Name
	var nicName string
	var err error
	nicID := (*vm.VirtualMachineProperties.NetworkProfile.NetworkInterfaces)[0].ID
	if nicID == nil {
		klog.Warningf("NIC ID is not set for VM (%s/%s)", rg, name)
	} else {
		nicName, err = resourceName(*nicID)
		if err != nil {
			return err
		}
		klog.Infof("found nic name for VM (%s/%s): %s", rg, name, nicName)
	}

	klog.Infof("deleting VM: %s/%s", rg, name)
	deleteCtx, deleteCancel := getContextWithCancel()
	defer deleteCancel()

	klog.Infof("waiting for VirtualMachine deletion: %s/%s", rg, name)
	rerr = util.manager.azClient.virtualMachinesClient.Delete(deleteCtx, rg, name)
	_, realErr := checkResourceExistsFromRetryError(rerr)
	if realErr != nil {
		return realErr
	}
	klog.V(2).Infof("VirtualMachine %s/%s removed", rg, name)

	if deleteNicErr := deleteNic(util.manager.azClient.interfacesClient, nicName, util.manager.config.ResourceGroup); deleteNicErr != nil {
		return deleteNicErr
	}

	if vhd != nil {
		return deleteVhdBlob(util.manager.azClient.storageAccountsClient, vhd, util.manager.env, util.manager.config.ResourceGroup,
			util.manager.config.SubscriptionID)
	} else if managedDisk != nil {
		return deleteManagedDisk(util.manager.azClient.disksClient, managedDisk, osDiskName, name, util.manager.config.ResourceGroup,
			util.manager.config.SubscriptionID)
	}
	return nil
}
