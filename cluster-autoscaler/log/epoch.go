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

package log

import "crypto/rand"

const (
	upperCaseAlphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
)

// getEpochRandomString generates a random string with the provided length using the given alphabet
func getEpochRandomString() (string, error) {
	randomBytes := make([]byte, 5)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", err
	}
	for index, randomByte := range randomBytes {
		foldedOffset := randomByte % byte(len(upperCaseAlphanumeric))
		randomBytes[index] = upperCaseAlphanumeric[foldedOffset]
	}
	return string(randomBytes), nil
}
