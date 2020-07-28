/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/interfaceclient"

	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/storageaccountclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"

	"k8s.io/autoscaler/cluster-autoscaler/version"
	"k8s.io/klog/v2"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute" //nolint SA1019 - deprecated package
	azStorage "github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
)

func deleteBlobCommon(storageAccountsClient storageaccountclient.Interface, accountName, vhdContainer, vhdBlob, resourceGroup,
	subscriptionID string, env *azure.Environment) error {
	ctx, cancel := getContextWithCancel()
	defer cancel()

	storageKeysResult, rerr := storageAccountsClient.ListKeys(ctx, subscriptionID, resourceGroup, accountName)
	if rerr != nil {
		return rerr.Error()
	}

	if env == nil {
		return fmt.Errorf("env cannot be nil while creating new basic client")
	}
	keys := *storageKeysResult.Keys
	client, err := azStorage.NewBasicClientOnSovereignCloud(accountName, to.String(keys[0].Value), *env)
	if err != nil {
		return err
	}

	bs := client.GetBlobService()
	containerRef := bs.GetContainerReference(vhdContainer)
	blobRef := containerRef.GetBlobReference(vhdBlob)

	return blobRef.Delete(&azStorage.DeleteBlobOptions{})
}

// k8sLinuxVMNameParts returns parts of Linux VM name e.g: k8s-agentpool1-11290731-0
func k8sLinuxVMNameParts(vmName string) (poolIdentifier, nameSuffix string, agentIndex int, err error) {
	vmNameParts := vmnameLinuxRegexp.FindStringSubmatch(vmName)
	if len(vmNameParts) != 4 {
		return "", "", -1, fmt.Errorf("resource name was missing from identifier")
	}

	vmNum, err := strconv.Atoi(vmNameParts[k8sLinuxVMAgentIndexArrayIndex])

	if err != nil {
		return "", "", -1, fmt.Errorf("error parsing VM Name: %v", err)
	}

	return vmNameParts[k8sLinuxVMAgentPoolNameIndex], vmNameParts[k8sLinuxVMAgentClusterIDIndex], vmNum, nil
}

// windowsVMNameParts returns parts of Windows VM name
func windowsVMNameParts(vmName string) (poolPrefix, orch string, poolIndex, agentIndex int, err error) {
	var poolInfo string
	vmNameParts := oldvmnameWindowsRegexp.FindStringSubmatch(vmName)
	if len(vmNameParts) != 5 {
		vmNameParts = vmnameWindowsRegexp.FindStringSubmatch(vmName)
		if len(vmNameParts) != 4 {
			return "", "", -1, -1, fmt.Errorf("resource name was missing from identifier")
		}
		poolInfo = vmNameParts[3]
	} else {
		poolInfo = vmNameParts[4]
	}

	poolPrefix = vmNameParts[1]
	orch = vmNameParts[2]

	poolIndex, err = strconv.Atoi(poolInfo[:2])
	if err != nil {
		return "", "", -1, -1, fmt.Errorf("error parsing VM Name: %v", err)
	}
	agentIndex, err = strconv.Atoi(poolInfo[2:])
	if err != nil {
		return "", "", -1, -1, fmt.Errorf("error parsing VM Name: %v", err)
	}

	return poolPrefix, orch, poolIndex, agentIndex, nil
}

// GetVMNameIndex return the index of VM in the node pools.
func GetVMNameIndex(osType compute.OperatingSystemTypes, vmName string) (int, error) {
	var agentIndex int
	var err error
	if osType == compute.OperatingSystemTypesLinux {
		_, _, agentIndex, err = k8sLinuxVMNameParts(vmName)
		if err != nil {
			return 0, err
		}
	} else if osType == compute.OperatingSystemTypesWindows {
		_, _, _, agentIndex, err = windowsVMNameParts(vmName)
		if err != nil {
			return 0, err
		}
	}

	return agentIndex, nil
}

// getLastSegment gets the last segment (splitting by '/'.)
func getLastSegment(id string) (string, error) {
	parts := strings.Split(strings.TrimSpace(id), "/")
	name := parts[len(parts)-1]
	if name == "" {
		return "", fmt.Errorf("identifier '/' not found in resource name %q", id)
	}

	return name, nil
}

// readDeploymentParameters gets deployment parameters from paramFilePath.
func readDeploymentParameters(paramFilePath string) (map[string]interface{}, error) {
	contents, err := os.ReadFile(paramFilePath)
	if err != nil {
		klog.Errorf("Failed to read deployment parameters from file %q: %v", paramFilePath, err)
		return nil, err
	}

	deploymentParameters := make(map[string]interface{})
	if err := json.Unmarshal(contents, &deploymentParameters); err != nil {
		klog.Errorf("Failed to unmarshal deployment parameters from file %q: %v", paramFilePath, err)
		return nil, err
	}

	if v, ok := deploymentParameters["parameters"]; ok {
		return v.(map[string]interface{}), nil
	}

	return nil, fmt.Errorf("failed to get deployment parameters from file %s", paramFilePath)
}

func getContextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func getContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func checkResourceExistsFromError(err error) error {
	if err == nil {
		return nil
	}
	v, ok := err.(autorest.DetailedError)
	if !ok {
		return err
	}
	if v.StatusCode == http.StatusNotFound {
		return nil
	}
	return v
}

func checkResourceExistsFromRetryError(err *retry.Error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if err.HTTPStatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, err.Error()
}

// isSuccessHTTPResponse determines if the response from an HTTP request suggests success
func isSuccessHTTPResponse(resp *http.Response, err error) (isSuccess bool, realError error) {
	if err != nil {
		return false, err
	}

	if resp != nil {
		// HTTP 2xx suggests a successful response
		if 199 < resp.StatusCode && resp.StatusCode < 300 {
			return true, nil
		}

		return false, fmt.Errorf("failed with HTTP status code %d", resp.StatusCode)
	}

	// This shouldn't happen, it only ensures all exceptions are handled.
	return false, fmt.Errorf("failed with unknown error")
}

// convertResourceGroupNameToLower converts the resource group name in the resource ID to be lowered.
func convertResourceGroupNameToLower(resourceID string) (string, error) {
	matches := azureResourceGroupNameRE.FindStringSubmatch(resourceID)
	if len(matches) != 2 {
		return "", fmt.Errorf("%q isn't in Azure resource ID format", resourceID)
	}

	resourceGroup := matches[1]
	return strings.Replace(resourceID, resourceGroup, strings.ToLower(resourceGroup), 1), nil
}

// isAzureRequestsThrottled returns true when the err is http.StatusTooManyRequests (429),
// and when err shows the requests was not executed due to an ongoing throttling period.
func isAzureRequestsThrottled(rerr *retry.Error) bool {
	klog.V(6).Infof("isAzureRequestsThrottled: starts for error %v", rerr)
	if rerr == nil {
		return false
	}

	if rerr.HTTPStatusCode == 0 && rerr.RetryAfter.After(time.Now()) {
		return true
	}

	return rerr.HTTPStatusCode == http.StatusTooManyRequests
}

// splitBlobURI returns a decomposed blob URI parts: accountName, containerName, blobName.
func splitBlobURI(uriStr string) (accountName, containerName, blobPath string, err error) {
	uri, err := url.Parse(uriStr)
	if err != nil {
		return "", "", "", err
	}

	accountName = strings.Split(uri.Host, ".")[0]
	urlParts := strings.Split(uri.Path, "/")

	containerName = urlParts[1]
	blobPath = strings.Join(urlParts[2:], "/")

	return accountName, containerName, blobPath, err
}

// resourceName returns the last segment (the resource name) for the specified resource identifier.
func resourceName(id string) (string, error) {
	parts := strings.Split(id, "/")
	name := parts[len(parts)-1]
	if name == "" {
		return "", fmt.Errorf("resource name was missing from identifier")
	}

	return name, nil
}

func getUserAgentExtension() string {
	return fmt.Sprintf("cluster-autoscaler-aks/v%s", version.ClusterAutoscalerVersion)
}

func configureUserAgent(client *autorest.Client) {
	client.UserAgent = fmt.Sprintf("%s; %s", client.UserAgent, getUserAgentExtension())
}

// normalizeForK8sVMASScalingUp takes a template and removes elements that are unwanted in a K8s VMAS scale up/down case
func normalizeForK8sVMASScalingUp(templateMap map[string]interface{}) error {
	if err := normalizeMasterResourcesForScaling(templateMap); err != nil {
		return err
	}
	rtIndex := -1
	nsgIndex := -1
	resources := templateMap[resourcesFieldName].([]interface{})
	for index, resource := range resources {
		resourceMap, ok := resource.(map[string]interface{})
		if !ok {
			klog.Warning("Template improperly formatted for resource")
			continue
		}

		resourceType, ok := resourceMap[typeFieldName].(string)
		if ok && resourceType == nsgResourceType {
			if nsgIndex != -1 {
				err := fmt.Errorf("found 2 resources with type %s in the template. There should only be 1", nsgResourceType)
				klog.Errorf(err.Error())
				return err
			}
			nsgIndex = index
		}
		if ok && resourceType == rtResourceType {
			if rtIndex != -1 {
				err := fmt.Errorf("found 2 resources with type %s in the template. There should only be 1", rtResourceType)
				klog.Warningf(err.Error())
				return err
			}
			rtIndex = index
		}

		dependencies, ok := resourceMap[dependsOnFieldName].([]interface{})
		if !ok {
			continue
		}

		for dIndex := len(dependencies) - 1; dIndex >= 0; dIndex-- {
			dependency := dependencies[dIndex].(string)
			if strings.Contains(dependency, nsgResourceType) || strings.Contains(dependency, nsgID) ||
				strings.Contains(dependency, rtResourceType) || strings.Contains(dependency, rtID) {
				dependencies = append(dependencies[:dIndex], dependencies[dIndex+1:]...)
			}
		}

		if len(dependencies) > 0 {
			resourceMap[dependsOnFieldName] = dependencies
		} else {
			delete(resourceMap, dependsOnFieldName)
		}
	}

	indexesToRemove := []int{}
	if nsgIndex == -1 {
		err := fmt.Errorf("found no resources with type %s in the template. There should have been 1", nsgResourceType)
		klog.Errorf(err.Error())
		return err
	}
	if rtIndex == -1 {
		klog.Infof("Found no resources with type %s in the template.", rtResourceType)
	} else {
		indexesToRemove = append(indexesToRemove, rtIndex)
	}
	indexesToRemove = append(indexesToRemove, nsgIndex)
	templateMap[resourcesFieldName] = removeIndexesFromArray(resources, indexesToRemove)

	return nil
}

func removeIndexesFromArray(array []interface{}, indexes []int) []interface{} {
	sort.Sort(sort.Reverse(sort.IntSlice(indexes)))
	for _, index := range indexes {
		array = append(array[:index], array[index+1:]...)
	}
	return array
}

// normalizeMasterResourcesForScaling takes a template and removes elements that are unwanted in any scale up/down case
func normalizeMasterResourcesForScaling(templateMap map[string]interface{}) error {
	resources := templateMap[resourcesFieldName].([]interface{})
	indexesToRemove := []int{}
	// update master nodes resources
	for index, resource := range resources {
		resourceMap, resourceMapOk := resource.(map[string]interface{})
		if !resourceMapOk {
			klog.Warning("Template improperly formatted")
			continue
		}

		resourceType, resourceTypeOk := resourceMap[typeFieldName].(string)
		if !resourceTypeOk || resourceType != vmResourceType {
			resourceName, resourceNameOk := resourceMap[nameFieldName].(string)
			if !resourceNameOk {
				klog.Warning("Template improperly formatted")
				continue
			}
			if strings.Contains(resourceName, "variables('masterVMNamePrefix')") && resourceType == vmExtensionType {
				indexesToRemove = append(indexesToRemove, index)
			}
			continue
		}

		resourceName, resourceNameOk := resourceMap[nameFieldName].(string)
		if !resourceNameOk {
			klog.Warning("Template improperly formatted")
			continue
		}

		// make sure this is only modifying the master vms
		if !strings.Contains(resourceName, "variables('masterVMNamePrefix')") {
			continue
		}

		resourceProperties, resourcePropertiesOk := resourceMap[propertiesFieldName].(map[string]interface{})
		if !resourcePropertiesOk {
			klog.Warning("Template improperly formatted")
			continue
		}

		hardwareProfile, hardwareProfileOk := resourceProperties[hardwareProfileFieldName].(map[string]interface{})
		if !hardwareProfileOk {
			klog.Warning("Template improperly formatted")
			continue
		}

		if hardwareProfile[vmSizeFieldName] != nil {
			delete(hardwareProfile, vmSizeFieldName)
		}

		if !removeCustomData(resourceProperties) || !removeImageReference(resourceProperties) {
			continue
		}
	}
	templateMap[resourcesFieldName] = removeIndexesFromArray(resources, indexesToRemove)

	return nil
}

func removeCustomData(resourceProperties map[string]interface{}) bool {
	osProfile, ok := resourceProperties[osProfileFieldName].(map[string]interface{})
	if !ok {
		klog.Warning("Template improperly formatted")
		return ok
	}

	if osProfile[customDataFieldName] != nil {
		delete(osProfile, customDataFieldName)
	}
	return ok
}

func removeImageReference(resourceProperties map[string]interface{}) bool {
	storageProfile, ok := resourceProperties[storageProfileFieldName].(map[string]interface{})
	if !ok {
		klog.Warningf("Template improperly formatted. Could not find: %s", storageProfileFieldName)
		return ok
	}

	if storageProfile[imageReferenceFieldName] != nil {
		delete(storageProfile, imageReferenceFieldName)
	}
	return ok
}

func deleteNic(interfacesClient interfaceclient.Interface, nicName, resourceGroup string) error {
	if nicName != "" {
		klog.Infof("deleting nic: %s/%s", resourceGroup, nicName)
		interfaceCtx, interfaceCancel := getContextWithCancel()
		defer interfaceCancel()
		rerr := interfacesClient.Delete(interfaceCtx, resourceGroup, nicName)
		klog.Infof("waiting for nic deletion: %s/%s", resourceGroup, nicName)
		_, realErr := checkResourceExistsFromRetryError(rerr)
		if realErr != nil {
			return realErr
		}
		klog.V(2).Infof("interface %s/%s removed", resourceGroup, nicName)
	}
	return nil
}

func deleteVhdBlob(storageAccountsClient storageaccountclient.Interface, vhd *compute.VirtualHardDisk, env *azure.Environment,
	resourceGroup, subscriptionID string) error {
	if vhd != nil {
		accountName, vhdContainer, vhdBlob, err := splitBlobURI(*vhd.URI)
		if err != nil {
			return err
		}
		klog.Infof("found os disk storage reference: %s %s %s", accountName, vhdContainer, vhdBlob)

		klog.Infof("deleting blob: %s/%s", vhdContainer, vhdBlob)
		err = deleteBlobCommon(storageAccountsClient, accountName, vhdContainer, vhdBlob, resourceGroup, subscriptionID, env)
		realErr := checkResourceExistsFromError(err)
		if realErr != nil {
			return realErr
		}
		klog.V(2).Infof("Blob %s/%s removed", resourceGroup, vhdBlob)
	}
	return nil
}

func deleteManagedDisk(diskClient diskclient.Interface, managedDisk *compute.ManagedDiskParameters, osDiskName *string,
	vmName, resourceGroup, subscriptionID string) error {
	if managedDisk != nil {
		if osDiskName == nil {
			klog.Warningf("osDisk is not set for VM %s/%s", resourceGroup, vmName)
		} else {
			klog.Infof("deleting managed disk: %s/%s", resourceGroup, *osDiskName)
			disksCtx, disksCancel := getContextWithCancel()
			defer disksCancel()
			diskErr := diskClient.Delete(disksCtx, subscriptionID, resourceGroup, *osDiskName)
			_, realErr := checkResourceExistsFromRetryError(diskErr)
			if realErr != nil {
				return realErr
			}
			klog.V(2).Infof("disk %s/%s removed", resourceGroup, *osDiskName)
		}
	}
	return nil
}

func powerStateDeallocating(state string) bool {
	return powerStateExpectedMatchesActual(vmPowerStateDeallocating, state)
}

func powerStateDeallocated(state string) bool {
	return powerStateExpectedMatchesActual(vmPowerStateDeallocated, state)
}

func powerStateExpectedMatchesActual(expected, actual string) bool {
	return strings.EqualFold(actual, expected)
}
