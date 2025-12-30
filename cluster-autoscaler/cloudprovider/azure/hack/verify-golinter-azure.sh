#!/bin/bash
# Copyright 2022 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

mode=${1:-"local"}

AZURE_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
cd "$AZURE_ROOT" # cluster-autoscaler/cloudprovider/azure

GOLANGCICMD_PATH=$(go env GOPATH)/bin/golangci-lint

# this line needs to be remove after https://github.com/golangci/golangci-lint/issues/3107 is fixed
GOROOT=$(go env GOROOT)
export GOROOT

# verify golangci-lint is installed
# binary will be $(go env GOPATH)/bin/golangci-lint
if ! command -v "$GOLANGCICMD_PATH" &> /dev/null
then
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)/bin" v1.56.2
fi

# run golangci-lint
if [ "$mode" == "pipeline" ]
then
    ${GOLANGCICMD_PATH} run --out-format junit-xml --fast=false --timeout 30m > junit.xml
else
    ${GOLANGCICMD_PATH} run -v --fast=false --timeout 30m
fi