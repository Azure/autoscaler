/*
Copyright 2020 The Kubernetes Authors.

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
	"encoding/json"
	"strings"

	"github.com/Azure/go-autorest/autorest/azure"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"
)

// Unknown is for errors that have nil RawError body
const Unknown errors.CloudProviderErrorReason = "Unknown"

// Errors on the sync path
const (
	// QuotaExceeded falls under OperationNotAllowed error code but we make it more specific here
	QuotaExceeded errors.CloudProviderErrorReason = "QuotaExceeded"
	// OperationNotAllowed is an umbrella for a lot of errors returned by Azure
	OperationNotAllowed string = "OperationNotAllowed"
)

// ServiceRawError wraps the RawError returned by the k8s/cloudprovider
// Azure clients. The error body  should satisfy the autorest.ServiceError type
type ServiceRawError struct {
	ServiceError *azure.ServiceError `json:"error,omitempty"`
}

func azureToAutoscalerError(rerr *retry.Error) errors.AutoscalerError {
	if rerr == nil {
		return nil
	}
	if rerr.RawError == nil {
		return errors.NewAutoscalerCloudProviderError(Unknown, rerr.Error().Error())
	}

	re := ServiceRawError{}
	err := json.Unmarshal([]byte(rerr.RawError.Error()), &re)
	if err != nil {
		return errors.NewAutoscalerCloudProviderError(Unknown, rerr.Error().Error())
	}
	se := re.ServiceError
	if se == nil {
		return errors.NewAutoscalerCloudProviderError(Unknown, rerr.Error().Error())
	}
	var errCode errors.CloudProviderErrorReason
	if se.Code == "" {
		errCode = Unknown
	} else if se.Code == OperationNotAllowed {
		errCode = getOperationNotAllowedReason(se)
	} else {
		errCode = errors.CloudProviderErrorReason(se.Code)
	}
	return errors.NewAutoscalerCloudProviderError(errCode, se.Message)
}

// getOperationNotAllowedReason renames the error code for quotas to a more human-readable error
func getOperationNotAllowedReason(se *azure.ServiceError) errors.CloudProviderErrorReason {
	if strings.Contains(se.Message, "Quota increase") {
		return QuotaExceeded
	}
	return errors.CloudProviderErrorReason(OperationNotAllowed)
}
